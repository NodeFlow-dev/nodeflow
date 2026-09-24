package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	mathrand "math/rand/v2"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Heartbeat struct {
	Version                  string    `json:"version"`
	Status                   string    `json:"status"`
	Metrics                  Stats     `json:"metrics"`
	RoutesOK                 bool      `json:"routes_ok"`
	ActualRevision           *int64    `json:"actual_revision,omitempty"`
	ConfigSHA256             string    `json:"config_sha256,omitempty"`
	TrafficInstanceID        string    `json:"traffic_instance_id"`
	TrafficInstanceStartedAt time.Time `json:"traffic_instance_started_at"`
	TrafficSampleSeq         int64     `json:"traffic_sample_seq"`
	HAProxyServiceState      string    `json:"haproxy_service_state,omitempty"`
	HAProxyControlGeneration int64     `json:"haproxy_control_generation"`
	HAProxyRestartGeneration int64     `json:"haproxy_restart_generation"`
	HAProxyControlError      string    `json:"haproxy_control_error,omitempty"`
}

type HeartbeatResponse struct {
	Assignment         *ConfigAssignment         `json:"assignment"`
	QuotaAssignment    *QuotaAssignment          `json:"quota_assignment"`
	FirewallAssignment *FirewallAssignment       `json:"firewall_assignment"`
	UpdateAssignment   *UpdateManifest           `json:"update_assignment"`
	ServiceAssignment  *HAProxyServiceAssignment `json:"service_assignment"`
}

type HeartbeatSender struct {
	URL            string
	Token          string
	Version        string
	Runner         Runner
	HAProxyBinary  string
	VersionSampler *HAProxyVersionSampler
	HAProxyRuntime HAProxyRuntimeCollector
	CPUUsage       *CPUUsageSampler
	NetworkRates   *NetworkRateSampler
	Processes      *ProcessSampler
	Client         *http.Client
	Logger         *log.Logger
	Reconciler     *Reconciler
	Quota          *QuotaReconciler
	// KernelPipes raises the kernel pipe limits on every heartbeat and
	// reports them as metrics.kernel_pipes (nil = not reported).
	KernelPipes      *KernelPipeTuner
	Firewall         *FirewallReconciler
	Updater          UpdateProcessor
	ServiceControl   *HAProxyServiceController
	ReconcileTimeout time.Duration
	QuotaTimeout     time.Duration
	FailureLogPeriod time.Duration
	Now              func() time.Time

	TrafficInstanceID        string
	TrafficInstanceStartedAt time.Time
	trafficOrderOnce         sync.Once
	trafficOrderErr          error
	trafficSampleSeq         atomic.Int64
	failureMu                sync.Mutex
	failures                 map[string]repeatedFailure
	lastRuntimeGeneration    atomic.Pointer[string]
}

type repeatedFailure struct {
	message    string
	lastLogged time.Time
	suppressed uint64
}

const defaultFailureLogPeriod = 5 * time.Minute

func (s *HeartbeatSender) logFailure(logger *log.Logger, key string, err error) {
	if err == nil {
		return
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	period := s.FailureLogPeriod
	if period <= 0 {
		period = defaultFailureLogPeriod
	}
	message := err.Error()
	s.failureMu.Lock()
	defer s.failureMu.Unlock()
	if s.failures == nil {
		s.failures = make(map[string]repeatedFailure)
	}
	state, exists := s.failures[key]
	if !exists || state.message != message || now.Sub(state.lastLogged) >= period {
		if state.suppressed > 0 {
			logger.Printf("%s failed: %s (suppressed %d repeats)", key, message, state.suppressed)
		} else {
			logger.Printf("%s failed: %s", key, message)
		}
		s.failures[key] = repeatedFailure{message: message, lastLogged: now}
		return
	}
	state.suppressed++
	s.failures[key] = state
}

func (s *HeartbeatSender) clearFailure(logger *log.Logger, key string) {
	s.failureMu.Lock()
	defer s.failureMu.Unlock()
	state, exists := s.failures[key]
	if !exists {
		return
	}
	delete(s.failures, key)
	if state.suppressed > 0 {
		logger.Printf("%s recovered after suppressing %d repeated errors", key, state.suppressed)
	}
}

func (s *HeartbeatSender) Send(ctx context.Context) error {
	_, err := s.send(ctx)
	return err
}

func (s *HeartbeatSender) send(ctx context.Context) (HeartbeatResponse, error) {
	if err := s.initializeTrafficOrder(); err != nil {
		return HeartbeatResponse{}, err
	}
	sampleSeq := s.trafficSampleSeq.Add(1)
	stats, err := CollectStats()
	if err != nil {
		return HeartbeatResponse{}, fmt.Errorf("collect metrics: %w", err)
	}
	now := time.Now()
	if s.CPUUsage != nil {
		stats.CPUPercent = s.CPUUsage.Sample(stats.cpuTotal, stats.cpuIdle)
	}
	if s.NetworkRates != nil {
		stats.NetworkRates = s.NetworkRates.Sample(stats.Network, now)
	}
	if s.Processes != nil {
		stats.ProcessNames = s.Processes.Sample(now)
	}
	if s.VersionSampler != nil {
		stats.HAProxyVersion = s.VersionSampler.Sample(ctx, now)
	} else if s.Runner != nil && s.HAProxyBinary != "" {
		stats.HAProxyVersion = HAProxyVersion(ctx, s.Runner, s.HAProxyBinary)
	}
	if s.HAProxyRuntime != nil {
		runtimeStats, runtimeErr := s.HAProxyRuntime.Collect(ctx)
		if runtimeErr == nil {
			stats.HAProxyStatsAvailable = true
			stats.HAProxyRuntime = &runtimeStats
			if s.Quota != nil {
				s.Quota.ObserveRuntime(runtimeStats)
			}
		}
		// The cached systemd state is only trusted while the runtime socket
		// keeps answering for the same HAProxy process generation. A missing
		// socket or a new pid/start time/reload count means the service may
		// have changed state behind the agent, so re-read it now.
		generation := ""
		if runtimeErr == nil {
			generation = runtimeStats.CounterGeneration
		}
		previous := s.lastRuntimeGeneration.Swap(&generation)
		if s.ServiceControl != nil && (generation == "" || previous == nil || *previous != generation) {
			s.ServiceControl.InvalidateState()
		}
	}
	if s.Quota != nil {
		stats.QuotaRuntime = s.Quota.Snapshot()
	}
	if s.Firewall != nil {
		stats.Firewall = s.Firewall.Snapshot()
	}
	if s.Updater != nil {
		stats.UpdateVerification = s.Updater.Snapshot()
	}
	if s.KernelPipes != nil {
		pipes := s.KernelPipes.Ensure()
		stats.KernelPipes = &pipes
	}
	serviceState := HAProxyServiceSnapshot{State: "unknown"}
	if s.ServiceControl != nil {
		serviceState = s.ServiceControl.Snapshot(ctx)
	}
	routesOK := s.HAProxyRuntime == nil || stats.HAProxyStatsAvailable || serviceState.State == "inactive"
	heartbeat := Heartbeat{
		Version: s.Version, Status: "online", Metrics: stats, RoutesOK: routesOK,
		TrafficInstanceID: s.TrafficInstanceID, TrafficInstanceStartedAt: s.TrafficInstanceStartedAt,
		TrafficSampleSeq:    sampleSeq,
		HAProxyServiceState: serviceState.State, HAProxyControlGeneration: serviceState.Generation,
		HAProxyRestartGeneration: serviceState.RestartGeneration,
		HAProxyControlError:      serviceState.LastError,
	}
	if s.Reconciler != nil && s.Reconciler.Manager != nil {
		observed := s.Reconciler.Manager.ObservedConfigState()
		heartbeat.ActualRevision = observed.ActualRevision
		heartbeat.ConfigSHA256 = observed.SHA256
	}
	body, err := json.Marshal(heartbeat)
	if err != nil {
		return HeartbeatResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL+"/agent/v1/heartbeat", bytes.NewReader(body))
	if err != nil {
		return HeartbeatResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	req.Header.Set("Content-Type", "application/json")
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return HeartbeatResponse{}, err
	}
	defer resp.Body.Close()
	const maxHeartbeatResponseBytes = (4 << 20) + 1
	responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxHeartbeatResponseBytes))
	if readErr != nil {
		return HeartbeatResponse{}, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return HeartbeatResponse{}, fmt.Errorf("panel returned HTTP %d", resp.StatusCode)
	}
	if len(responseBody) == 0 {
		return HeartbeatResponse{}, nil
	}
	if len(responseBody) == maxHeartbeatResponseBytes {
		return HeartbeatResponse{}, fmt.Errorf("panel heartbeat response exceeds size limit")
	}
	var response HeartbeatResponse
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return HeartbeatResponse{}, fmt.Errorf("invalid panel heartbeat response")
	}
	return response, nil
}

func (s *HeartbeatSender) Run(ctx context.Context, interval time.Duration) {
	if s.URL == "" {
		return
	}
	logger := s.Logger
	if logger == nil {
		logger = log.Default()
	}
	send := func() {
		response, err := s.send(ctx)
		if err != nil && ctx.Err() == nil {
			s.logFailure(logger, "heartbeat", err)
			return
		}
		s.clearFailure(logger, "heartbeat")
		skipQuota := false
		configApplied := false
		configApplyAllowed := true
		if response.ServiceAssignment != nil && s.ServiceControl != nil {
			serviceCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			err = s.ServiceControl.Reconcile(serviceCtx, *response.ServiceAssignment)
			cancel()
			if err != nil && ctx.Err() == nil {
				s.logFailure(logger, "HAProxy service reconciliation", err)
				configApplyAllowed = false
			} else if err == nil {
				s.clearFailure(logger, "HAProxy service reconciliation")
			}
			if !response.ServiceAssignment.Enabled {
				configApplyAllowed = false
				skipQuota = true
			}
		}
		firewallHandled := false
		transitionPrepared := false
		var activeFirewall, desiredFirewall FirewallAssignment
		if response.Assignment != nil && s.Reconciler != nil && response.FirewallAssignment != nil &&
			response.FirewallAssignment.Transition && response.FirewallAssignment.Mode == FirewallModeApply {
			if s.Firewall == nil {
				configApplyAllowed = false
				firewallHandled = true
				if ctx.Err() == nil {
					logger.Printf("config reconciliation deferred: firewall transition is required but not configured")
				}
			} else {
				var preOpen FirewallAssignment
				preOpen, activeFirewall, desiredFirewall, err = firewallTransitionAssignments(*response.FirewallAssignment)
				if err == nil {
					firewallCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
					_, err = s.Firewall.PreOpen(firewallCtx, preOpen)
					cancel()
				}
				if err != nil {
					configApplyAllowed = false
					firewallHandled = true
					if ctx.Err() == nil {
						logger.Printf("config reconciliation deferred: firewall pre-open failed: %v", err)
					}
					// A failed pre-open may have added a subset of desired ports.
					// Restore the exact active set before waiting for a retry.
					if activeFirewall.Mode != "" && response.FirewallAssignment.ActivePlanComplete {
						firewallCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
						_, cleanupErr := s.Firewall.Reconcile(firewallCtx, activeFirewall)
						cancel()
						if cleanupErr != nil && ctx.Err() == nil {
							logger.Printf("firewall pre-open rollback failed: %v", cleanupErr)
						}
					}
				} else {
					transitionPrepared = true
				}
			}
		}
		if response.Assignment != nil && s.Reconciler != nil && configApplyAllowed {
			timeout := s.ReconcileTimeout
			if timeout <= 0 {
				timeout = 45 * time.Second
			}
			reconcileCtx, cancel := context.WithTimeout(ctx, timeout)
			err = s.Reconciler.Reconcile(reconcileCtx, *response.Assignment)
			cancel()
			if s.Quota != nil {
				s.Quota.Reset()
			}
			configApplied = err == nil
			if !configApplied && s.Reconciler.Manager != nil {
				// Reconcile can return an error after HAProxy was successfully
				// activated when only the final Panel report failed. The local
				// marker+hash remain authoritative for deciding whether this
				// response's pre-apply quota assignment is stale.
				observed := s.Reconciler.Manager.ObservedConfigState()
				configApplied = observed.ActualRevision != nil &&
					*observed.ActualRevision == response.Assignment.Revision &&
					strings.EqualFold(observed.SHA256, response.Assignment.SHA256)
			}
			if err != nil && ctx.Err() == nil {
				logger.Printf("config reconciliation failed: %v", err)
			}
			if configApplied {
				// The quota assignment in this response describes the revision that
				// was active before this successful reload. Wait for the next
				// heartbeat to receive policy for the newly observed revision.
				skipQuota = true
			}
		}
		if transitionPrepared {
			firewallHandled = true
			if configApplied || response.FirewallAssignment.ActivePlanComplete {
				finalFirewall := activeFirewall
				if configApplied {
					finalFirewall = desiredFirewall
				}
				firewallCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				_, firewallErr := s.Firewall.Reconcile(firewallCtx, finalFirewall)
				cancel()
				if firewallErr != nil && ctx.Err() == nil {
					logger.Printf("firewall transition finalization failed: %v", firewallErr)
				}
			} else if ctx.Err() == nil {
				logger.Printf("firewall transition rollback kept pre-opened ports because the active legacy listener plan is incomplete")
			}
		}
		if !skipQuota && response.QuotaAssignment != nil && s.Quota != nil {
			timeout := s.QuotaTimeout
			if timeout <= 0 {
				timeout = 10 * time.Second
			}
			quotaCtx, cancel := context.WithTimeout(ctx, timeout)
			err = s.Quota.Reconcile(quotaCtx, *response.QuotaAssignment)
			cancel()
			if err != nil && ctx.Err() == nil {
				logger.Printf("quota reconciliation failed: %v", err)
			}
		}
		if !firewallHandled && response.FirewallAssignment != nil && s.Firewall != nil {
			firewallCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			_, err = s.Firewall.Reconcile(firewallCtx, *response.FirewallAssignment)
			cancel()
			if err != nil && ctx.Err() == nil {
				s.logFailure(logger, "firewall reconciliation", err)
			} else if err == nil {
				s.clearFailure(logger, "firewall reconciliation")
			}
		}
		if response.UpdateAssignment != nil && s.Updater != nil {
			updateCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			_, err = s.Updater.Process(updateCtx, *response.UpdateAssignment)
			cancel()
			if err != nil && ctx.Err() == nil {
				logger.Printf("agent update verification failed: %v", err)
			}
		}
	}
	send()
	// Each period is interval ±10% so agents started together (for example by
	// a fleet rollout) drift apart instead of hitting the Panel in lockstep.
	// The average period stays equal to interval. The ticker is re-armed
	// before send, so, as before, a slow cycle is followed by at most one
	// immediate catch-up tick rather than a burst.
	ticker := time.NewTicker(jitteredInterval(interval, mathrand.Float64()))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ticker.Reset(jitteredInterval(interval, mathrand.Float64()))
			send()
		}
	}
}

const heartbeatJitterFraction = 0.1

// jitteredInterval maps unit in [0,1) to interval scaled by [0.9, 1.1).
func jitteredInterval(interval time.Duration, unit float64) time.Duration {
	if interval <= 0 {
		// time.NewTicker panics on non-positive intervals; keep that contract.
		panic("non-positive interval for heartbeat")
	}
	jittered := time.Duration(float64(interval) * (1 - heartbeatJitterFraction + 2*heartbeatJitterFraction*unit))
	if jittered <= 0 {
		return interval
	}
	return jittered
}

func firewallTransitionAssignments(assignment FirewallAssignment) (preOpen, active, desired FirewallAssignment, err error) {
	if !assignment.Transition {
		return FirewallAssignment{}, FirewallAssignment{}, FirewallAssignment{}, errors.New("firewall assignment is not a transition")
	}
	activePorts, err := validateFirewallTCPPorts(assignment.TCPPorts)
	if err != nil {
		return FirewallAssignment{}, FirewallAssignment{}, FirewallAssignment{}, err
	}
	desiredPorts, err := validateFirewallTCPPorts(assignment.DesiredTCPPorts)
	if err != nil {
		return FirewallAssignment{}, FirewallAssignment{}, FirewallAssignment{}, err
	}
	union := make(map[int]struct{}, len(activePorts)+len(desiredPorts))
	for _, port := range activePorts {
		union[port] = struct{}{}
	}
	for _, port := range desiredPorts {
		union[port] = struct{}{}
	}
	if len(union) > maxFirewallTCPPorts {
		return FirewallAssignment{}, FirewallAssignment{}, FirewallAssignment{}, errors.New("too many firewall TCP ports")
	}
	unionPorts := make([]int, 0, len(union))
	for port := range union {
		unionPorts = append(unionPorts, port)
	}
	sort.Ints(unionPorts)
	active = FirewallAssignment{Mode: assignment.Mode, TCPPorts: activePorts, ActivePlanComplete: assignment.ActivePlanComplete}
	desired = FirewallAssignment{Mode: assignment.Mode, TCPPorts: desiredPorts}
	preOpen = FirewallAssignment{Mode: assignment.Mode, TCPPorts: unionPorts}
	return preOpen, active, desired, nil
}

func (s *HeartbeatSender) initializeTrafficOrder() error {
	s.trafficOrderOnce.Do(func() {
		if s.TrafficInstanceID == "" {
			s.TrafficInstanceID, s.trafficOrderErr = newTrafficInstanceID()
		}
		if s.TrafficInstanceStartedAt.IsZero() {
			s.TrafficInstanceStartedAt = time.Now().UTC().Truncate(time.Microsecond)
		} else {
			s.TrafficInstanceStartedAt = s.TrafficInstanceStartedAt.UTC().Truncate(time.Microsecond)
		}
	})
	return s.trafficOrderErr
}

func newTrafficInstanceID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate traffic instance ID: %w", err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(raw[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}
