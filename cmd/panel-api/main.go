package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nodeflow/nodeflow/internal/bootstrap"
	"github.com/nodeflow/nodeflow/internal/panel"
)

// panelVersion is the published Panel release version. Release builds may
// replace it with -ldflags "-X main.panelVersion=...".
var panelVersion = "2.0.2"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "migrate":
			os.Exit(runMigrate())
		case "version", "-version", "--version":
			fmt.Println(panelVersion)
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown command %q; usage: panel-api [migrate|version]\n", os.Args[1])
			os.Exit(2)
		}
	}
	cfg, err := panel.LoadConfig()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	cfg.PanelVersion = panelVersion
	retentionPolicy, err := panel.LoadDataRetentionPolicy()
	if err != nil {
		slog.Error("invalid retention configuration", "error", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		slog.Error("parse database URL", "error", err)
		os.Exit(1)
	}
	poolCfg.MaxConns = cfg.DatabaseMaxConns
	if poolCfg.MaxConns >= 10 {
		// Keep a few warm connections so bursts of heartbeats and UI reads do
		// not pay connection setup latency.
		poolCfg.MinConns = 2
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		slog.Error("open database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		slog.Error("database unavailable", "error", err)
		os.Exit(1)
	}

	store := panel.NewPGStore(pool)
	auditCleanerDone := make(chan struct{})
	go func() {
		defer close(auditCleanerDone)
		panel.RunAuditRetentionCleaner(ctx, store)
	}()
	dataCleanerDone := make(chan struct{})
	go func() {
		defer close(dataCleanerDone)
		panel.RunDataRetentionCleaner(ctx, store, retentionPolicy)
	}()
	var releases *panel.ReleaseService
	if cfg.UpdateSigningKeyFile != "" {
		releases, err = panel.NewReleaseService(cfg.AgentReleaseDir, cfg.UpdateSigningKeyFile, store)
		if err != nil {
			slog.Error("load Agent release service", "error", err)
			os.Exit(1)
		}
		defer releases.Close()
	}
	installer := bootstrap.NewSSHInstaller(cfg.AgentPublicURL)
	installer.UpdaterBinaryPath = os.Getenv("PANEL_NODE_UPDATER_BINARY")
	if releases != nil {
		installer.UpdatePublicKey = releases.PublicKeyBase64()
		installer.Releases = releases
	}
	var credentialIssuer bootstrap.CSRIdentityIssuer
	if cfg.AgentTLSIssuerKeyFile != "" {
		issuer, err := bootstrap.LoadMTLSIssuer(cfg.AgentTLSClientCAFile, cfg.AgentTLSIssuerKeyFile, cfg.AgentTLSServerName)
		if err != nil {
			slog.Error("load Agent certificate issuer", "error", err)
			os.Exit(1)
		}
		installer.Issuer = issuer
		credentialIssuer = issuer
	}
	handler := panel.NewHandlerWithServicesAndIssuer(store, cfg, installer, releases, credentialIssuer)
	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		// Dev bootstrap may include apt installation over SSH.
		WriteTimeout:   10 * time.Minute,
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}
	servers := []*http.Server{server}

	agentTLSConfig, err := panel.LoadAgentTLSConfig(cfg)
	if err != nil {
		slog.Error("load Agent mTLS configuration", "error", err)
		os.Exit(1)
	}
	if agentTLSConfig != nil {
		agentServer := &http.Server{
			Addr:              cfg.AgentTLSListenAddr,
			Handler:           handler,
			TLSConfig:         agentTLSConfig,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			// Signed Agent artifacts may be up to 64 MiB on slower links.
			WriteTimeout:   10 * time.Minute,
			IdleTimeout:    60 * time.Second,
			MaxHeaderBytes: 1 << 20,
		}
		servers = append(servers, agentServer)
		go func() {
			slog.Info("Agent mTLS API listening", "address", cfg.AgentTLSListenAddr)
			if err := agentServer.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("Agent mTLS server failed", "error", err)
				cancel()
			}
		}()
	}

	go func() {
		slog.Info("panel API listening", "address", cfg.ListenAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("HTTP server failed", "error", err)
			cancel()
		}
	}()

	<-ctx.Done()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	for _, runningServer := range servers {
		if err := runningServer.Shutdown(shutdownCtx); err != nil {
			slog.Error("HTTP shutdown failed", "address", runningServer.Addr, "error", err)
		}
	}
	select {
	case <-auditCleanerDone:
	case <-shutdownCtx.Done():
		slog.Warn("audit retention cleaner did not stop before shutdown deadline")
	}
	select {
	case <-dataCleanerDone:
	case <-shutdownCtx.Done():
		slog.Warn("data retention cleaner did not stop before shutdown deadline")
	}
}
