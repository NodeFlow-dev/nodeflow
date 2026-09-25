package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/nodeflow/nodeflow/internal/agent"
)

var version = "2.0.1"

func main() {
	showVersion := flag.Bool("version", false, "print Node Agent version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	if flag.NArg() != 0 {
		log.Fatalf("unexpected arguments: %v", flag.Args())
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	cfg := agent.ConfigFromEnv()
	if err := cfg.ValidateListenAddress(); err != nil {
		log.Fatalf("invalid NODE_AGENT_LISTEN: %v", err)
	}
	if cfg.Token == "" {
		log.Fatal("NODE_AGENT_TOKEN is required")
	}
	credentialManager, panelClient, err := agent.NewPanelCredentialManager(cfg)
	if err != nil {
		log.Fatalf("invalid Panel credential configuration: %v", err)
	}
	if credentialManager != nil {
		go credentialManager.Run(ctx)
	}
	runner := agent.ExecRunner{}
	manager := &agent.ConfigManager{Runner: runner, ManagedConfig: cfg.ManagedConfig, HAProxyBinary: cfg.HAProxyBinary, ServiceName: cfg.ServiceName}
	installedState, err := agent.LoadInstalledUpdateState(cfg.UpdateStateFile)
	if err != nil {
		log.Fatalf("invalid installed update state: %v", err)
	}
	if installedState.Sequence > cfg.UpdateSequence {
		cfg.UpdateSequence = installedState.Sequence
	}
	verifier, err := agent.NewUpdateVerifier(agent.UpdateVerifierConfig{Mode: cfg.SelfUpdateMode, StagingDir: cfg.UpdateStagingDir, PublicKeyBase64: cfg.UpdatePublicKey, CurrentSequence: cfg.UpdateSequence})
	if err != nil {
		log.Fatalf("invalid self-update configuration: %v", err)
	}
	updater, err := agent.NewUpdateCoordinator(agent.UpdateCoordinatorConfig{
		Verifier: verifier, Client: panelClient, PanelURL: cfg.PanelURL, Token: cfg.Token,
		StagingDir: cfg.UpdateStagingDir, PendingFile: cfg.UpdatePendingFile, StateFile: cfg.UpdateStateFile,
		ResultFile: cfg.UpdateResultFile, HelperService: cfg.UpdateHelperService, Runner: runner,
	})
	if err != nil {
		log.Fatalf("invalid update coordinator configuration: %v", err)
	}
	agentServer := agent.NewServer(cfg, manager, version)
	agentServer.Updater = verifier
	server := &http.Server{Addr: cfg.ListenAddr, Handler: agentServer.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	reconciler := &agent.Reconciler{Manager: manager, Reporter: agent.ConfigReporter{URL: cfg.PanelURL, Token: cfg.Token, Client: panelClient}}
	runtimeStats := agent.HAProxySocketClient{Path: cfg.HAProxyStatsSocket, Timeout: cfg.HAProxyStatsTimeout}
	// Hostnames in accept_proxy_from: resolved into the pp-trusted ACL files
	// before every HAProxy validation and kept current at runtime.
	ppTrusted := agent.NewPPTrustedController(manager, runtimeStats, net.DefaultResolver)
	versionSampler := &agent.HAProxyVersionSampler{Runner: runner, Binary: cfg.HAProxyBinary}
	quota := &agent.QuotaReconciler{Controller: runtimeStats, Manager: manager}
	firewall := &agent.FirewallReconciler{Config: agent.FirewallConfig{Mode: cfg.FirewallMode}, Runner: runner, StatusCacheTTL: 5 * time.Minute}
	serviceControl := &agent.HAProxyServiceController{Runner: runner, Manager: manager, ServiceName: cfg.ServiceName}
	// Kernel pipe limits for 1 MiB splice pipes (NODE_AGENT_TUNE_SYSCTL=off
	// only reports). Applied now and re-checked on every heartbeat.
	kernelPipes := agent.NewKernelPipeTunerFromEnv()
	if state := kernelPipes.Ensure(); state.Error != "" {
		log.Printf("kernel pipe limits: %s", state.Error)
	}
	heartbeatSender := &agent.HeartbeatSender{KernelPipes: kernelPipes, URL: cfg.PanelURL, Token: cfg.Token, Version: version, VersionSampler: versionSampler, HAProxyRuntime: runtimeStats, CPUUsage: &agent.CPUUsageSampler{}, NetworkRates: &agent.NetworkRateSampler{}, Processes: &agent.ProcessSampler{}, Client: panelClient, Reconciler: reconciler, Quota: quota, Firewall: firewall, Updater: updater, ServiceControl: serviceControl, ReconcileTimeout: cfg.ReconcileTimeout}
	go heartbeatSender.Run(ctx, cfg.HeartbeatInterval)
	// Kernel per-client shaper for routes with shaper_mode=kernel. Mode from
	// NODE_AGENT_KERNEL_SHAPER=apply|dry-run|off (default apply).
	go agent.NewKernelShaperFromEnv(runner, manager, reconciler.Reporter).Run(ctx, 5*time.Second)
	// Runtime server weights for balance_algorithm=leastping and DNS-pool
	// ip_weights. NODE_AGENT_WEIGHTS=apply|off (default apply).
	go agent.NewWeightControllerFromEnv(runtimeStats, manager).Run(ctx, 5*time.Second)
	go ppTrusted.Run(ctx, agent.PPTrustedInterval)
	log.Printf("node-agent %s listening on %s", version, cfg.ListenAddr)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown failed: %v", err)
		}
	}()
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
