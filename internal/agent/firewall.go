package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	FirewallModeOff     = "off"
	FirewallModeObserve = "observe"
	FirewallModeApply   = "apply"

	maxFirewallTCPPorts = 2048
	firewallRuleComment = "nodeflow"
)

// FirewallConfig is a local safety ceiling. A remote assignment can request a
// less privileged mode, but it cannot raise the agent above this mode.
type FirewallConfig struct {
	Mode string
}

type FirewallAssignment struct {
	Mode               string `json:"mode"`
	TCPPorts           []int  `json:"tcp_ports"`
	DesiredTCPPorts    []int  `json:"desired_tcp_ports,omitempty"`
	Transition         bool   `json:"transition,omitempty"`
	ActivePlanComplete bool   `json:"active_plan_complete,omitempty"`
}

// FirewallStatus is intentionally a summary: raw UFW output can contain host
// addresses and is neither needed by the panel nor safe to persist by default.
type FirewallStatus struct {
	LocalMode     string `json:"local_mode"`
	RequestedMode string `json:"requested_mode,omitempty"`
	EffectiveMode string `json:"effective_mode"`
	UFWStatus     string `json:"ufw_status"`
	UFWAvailable  bool   `json:"ufw_available"`
	Active        bool   `json:"active"`
	TCPPorts      []int  `json:"tcp_ports,omitempty"`
	ManagedPorts  []int  `json:"managed_tcp_ports,omitempty"`
	StalePorts    []int  `json:"stale_tcp_ports,omitempty"`
	Applied       int    `json:"applied"`
	Removed       int    `json:"removed"`
}

// FirewallReconciler only manages TCP allow rules carrying the exact
// nodeflow comment. It never installs, enables, resets or reloads UFW,
// and never removes an untagged operator rule.
type FirewallReconciler struct {
	Config FirewallConfig
	Runner Runner
	// StatusCacheTTL enables reuse of the last `ufw status numbered` output
	// while the UFW rule files are unchanged. `ufw` is a Python program costing
	// ~0.1 s CPU per call; without the cache every heartbeat paid it. Zero
	// disables the cache (tests and callers that need a fresh read).
	StatusCacheTTL time.Duration
	// StatusCacheFiles are fingerprinted (size, mtime, inode) to detect rule
	// changes made outside the agent. Defaults to the standard UFW files.
	StatusCacheFiles []string

	mu         sync.Mutex
	statusMu   sync.Mutex
	lastStatus *FirewallStatus

	cachedOutput      []byte
	cachedFingerprint string
	cachedAt          time.Time
}

var defaultUFWStatusFiles = []string{
	"/etc/ufw/user.rules", "/etc/ufw/user6.rules", "/etc/ufw/ufw.conf",
	"/etc/default/ufw",
}

// ufwFingerprint returns "" when any file cannot be stat'ed, which disables
// the cache so an unexpected layout never serves stale data.
func (r *FirewallReconciler) ufwFingerprint() string {
	files := r.StatusCacheFiles
	if len(files) == 0 {
		files = defaultUFWStatusFiles
	}
	var b strings.Builder
	for _, path := range files {
		info, err := os.Stat(path)
		if err != nil {
			return ""
		}
		var ino uint64
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			ino = st.Ino
		}
		fmt.Fprintf(&b, "%s:%d:%d:%d;", path, info.Size(), info.ModTime().UnixNano(), ino)
	}
	return b.String()
}

// ufwStatusOutput runs `ufw status numbered`, or reuses the cached output
// when the cache is enabled, fresh and the rule files are unchanged.
func (r *FirewallReconciler) ufwStatusOutput(ctx context.Context) ([]byte, error) {
	if r.StatusCacheTTL <= 0 {
		return r.Runner.Run(ctx, "ufw", "status", "numbered")
	}
	fp := r.ufwFingerprint()
	if fp != "" && r.cachedOutput != nil && fp == r.cachedFingerprint && time.Since(r.cachedAt) < r.StatusCacheTTL {
		return append([]byte(nil), r.cachedOutput...), nil
	}
	output, err := r.Runner.Run(ctx, "ufw", "status", "numbered")
	if err != nil || fp == "" {
		r.invalidateStatusCache()
		return output, err
	}
	r.cachedOutput = append([]byte(nil), output...)
	r.cachedFingerprint = fp
	r.cachedAt = time.Now()
	return output, nil
}

func (r *FirewallReconciler) invalidateStatusCache() {
	r.cachedOutput = nil
	r.cachedFingerprint = ""
}

func (r *FirewallReconciler) Reconcile(ctx context.Context, assignment FirewallAssignment) (FirewallStatus, error) {
	return r.reconcile(ctx, assignment, true)
}

// PreOpen adds missing managed listener rules without removing any existing
// rule. It is used before a HAProxy reload so both the current and desired
// listeners remain reachable throughout the transition.
func (r *FirewallReconciler) PreOpen(ctx context.Context, assignment FirewallAssignment) (FirewallStatus, error) {
	return r.reconcile(ctx, assignment, false)
}

func (r *FirewallReconciler) reconcile(ctx context.Context, assignment FirewallAssignment, prune bool) (FirewallStatus, error) {
	if r == nil || r.Runner == nil {
		return FirewallStatus{}, errors.New("firewall reconciler is not configured")
	}
	localMode, err := normalizeLocalFirewallMode(r.Config.Mode)
	if err != nil {
		return FirewallStatus{}, err
	}
	requestedMode, err := validateFirewallMode(assignment.Mode)
	if err != nil {
		return FirewallStatus{}, err
	}
	ports, err := validateFirewallTCPPorts(assignment.TCPPorts)
	if err != nil {
		return FirewallStatus{}, err
	}

	effectiveMode := lowerFirewallMode(localMode, requestedMode)
	status := FirewallStatus{
		LocalMode:     localMode,
		RequestedMode: requestedMode,
		EffectiveMode: effectiveMode,
		UFWStatus:     "not_checked",
		TCPPorts:      ports,
	}
	if effectiveMode == FirewallModeOff {
		status.UFWStatus = "disabled"
		r.publish(status)
		return status, nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	observed, managedRules, err := r.statusLocked(ctx, localMode)
	observed.RequestedMode = requestedMode
	observed.EffectiveMode = effectiveMode
	observed.TCPPorts = ports
	observed.ManagedPorts = managedRulePorts(managedRules)
	observed.StalePorts = staleManagedPorts(observed.ManagedPorts, ports)
	if err != nil {
		r.publish(observed)
		return observed, err
	}
	if effectiveMode != FirewallModeApply {
		r.publish(observed)
		return observed, nil
	}
	if !observed.Active || observed.UFWStatus != "active" {
		r.publish(observed)
		return observed, errors.New("refusing to apply firewall rules: UFW is not active")
	}

	desired := make(map[int]struct{}, len(ports))
	for _, port := range ports {
		desired[port] = struct{}{}
	}
	if prune {
		staleRules := make([]managedUFWRule, 0)
		for _, rule := range managedRules {
			if _, keep := desired[rule.Port]; !keep {
				staleRules = append(staleRules, rule)
			}
		}
		sort.Slice(staleRules, func(i, j int) bool { return staleRules[i].Number > staleRules[j].Number })
		for _, rule := range staleRules {
			r.invalidateStatusCache()
			output, runErr := r.Runner.Run(ctx, "ufw", "--force", "delete", strconv.Itoa(rule.Number))
			if runErr != nil {
				r.publish(observed)
				return observed, fmt.Errorf("delete stale nodeflow UFW rule %d: %w", rule.Number, sanitizedRunnerError(output, runErr))
			}
			observed.Removed++
		}
	}
	existing := make(map[int]struct{}, len(managedRules))
	for _, rule := range managedRules {
		existing[rule.Port] = struct{}{}
	}
	for _, port := range ports {
		if _, exists := existing[port]; exists {
			continue
		}
		portSpec := strconv.Itoa(port) + "/tcp"
		r.invalidateStatusCache()
		output, runErr := r.Runner.Run(ctx, "ufw", "allow", portSpec, "comment", firewallRuleComment)
		if runErr != nil {
			r.publish(observed)
			return observed, fmt.Errorf("add UFW allow rule for TCP port %d: %w", port, sanitizedRunnerError(output, runErr))
		}
		observed.Applied++
	}
	if prune {
		observed.ManagedPorts = append([]int(nil), ports...)
		observed.StalePorts = nil
	} else {
		managed := make(map[int]struct{}, len(observed.ManagedPorts)+len(ports))
		for _, port := range observed.ManagedPorts {
			managed[port] = struct{}{}
		}
		for _, port := range ports {
			managed[port] = struct{}{}
		}
		observed.ManagedPorts = observed.ManagedPorts[:0]
		for port := range managed {
			observed.ManagedPorts = append(observed.ManagedPorts, port)
		}
		sort.Ints(observed.ManagedPorts)
		observed.StalePorts = staleManagedPorts(observed.ManagedPorts, ports)
	}
	r.publish(observed)
	return observed, nil
}

func (r *FirewallReconciler) Status(ctx context.Context) (FirewallStatus, error) {
	if r == nil || r.Runner == nil {
		return FirewallStatus{}, errors.New("firewall reconciler is not configured")
	}
	localMode, err := normalizeLocalFirewallMode(r.Config.Mode)
	if err != nil {
		return FirewallStatus{}, err
	}
	if localMode == FirewallModeOff {
		status := FirewallStatus{
			LocalMode:     localMode,
			EffectiveMode: FirewallModeOff,
			UFWStatus:     "disabled",
		}
		r.publish(status)
		return status, nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	status, _, err := r.statusLocked(ctx, localMode)
	r.publish(status)
	return status, err
}

type managedUFWRule struct {
	Number int
	Port   int
}

func (r *FirewallReconciler) statusLocked(ctx context.Context, localMode string) (FirewallStatus, []managedUFWRule, error) {
	status := FirewallStatus{
		LocalMode:     localMode,
		EffectiveMode: localMode,
		UFWStatus:     "error",
	}
	output, err := r.ufwStatusOutput(ctx)
	if err != nil {
		var notFound *exec.Error
		if errors.As(err, &notFound) {
			status.UFWStatus = "not_installed"
			return status, nil, nil
		}
		return status, nil, fmt.Errorf("read UFW status: %w", sanitizedRunnerError(output, err))
	}
	status.UFWAvailable = true
	switch parseUFWStatus(output) {
	case "active":
		status.UFWStatus = "active"
		status.Active = true
	case "inactive":
		status.UFWStatus = "inactive"
	default:
		status.UFWStatus = "unknown"
	}
	rules := parseManagedUFWRules(output)
	status.ManagedPorts = managedRulePorts(rules)
	return status, rules, nil
}

func parseManagedUFWRules(output []byte) []managedUFWRule {
	rules := make([]managedUFWRule, 0)
	for _, rawLine := range strings.Split(strings.ReplaceAll(string(output), "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(rawLine)
		if !strings.HasPrefix(line, "[") {
			continue
		}
		closeBracket := strings.IndexByte(line, ']')
		if closeBracket < 2 {
			continue
		}
		number, err := strconv.Atoi(strings.TrimSpace(line[1:closeBracket]))
		if err != nil || number < 1 {
			continue
		}
		ruleText, comment, hasComment := strings.Cut(strings.TrimSpace(line[closeBracket+1:]), "#")
		if !hasComment || strings.TrimSpace(comment) != firewallRuleComment {
			continue
		}
		fields := strings.Fields(ruleText)
		// Only the exact inbound TCP allow shape emitted by Reconcile belongs to
		// the Agent. A deny/outbound rule is never adopted merely because an
		// operator reused the comment.
		actionIndex := 1
		if len(fields) > 1 && fields[1] == "(v6)" {
			actionIndex = 2
		}
		if len(fields) <= actionIndex+1 || !strings.HasSuffix(fields[0], "/tcp") || fields[actionIndex] != "ALLOW" || fields[actionIndex+1] != "IN" {
			continue
		}
		port, err := strconv.Atoi(strings.TrimSuffix(fields[0], "/tcp"))
		if err != nil || port < 1 || port > 65535 {
			continue
		}
		rules = append(rules, managedUFWRule{Number: number, Port: port})
	}
	return rules
}

func managedRulePorts(rules []managedUFWRule) []int {
	ports := make([]int, 0, len(rules))
	seen := make(map[int]struct{}, len(rules))
	for _, rule := range rules {
		if _, exists := seen[rule.Port]; exists {
			continue
		}
		seen[rule.Port] = struct{}{}
		ports = append(ports, rule.Port)
	}
	sort.Ints(ports)
	return ports
}

func staleManagedPorts(managed, desired []int) []int {
	wanted := make(map[int]struct{}, len(desired))
	for _, port := range desired {
		wanted[port] = struct{}{}
	}
	stale := make([]int, 0)
	for _, port := range managed {
		if _, exists := wanted[port]; !exists {
			stale = append(stale, port)
		}
	}
	return stale
}

func (r *FirewallReconciler) publish(status FirewallStatus) {
	if r == nil {
		return
	}
	r.statusMu.Lock()
	defer r.statusMu.Unlock()
	copy := status
	copy.TCPPorts = append([]int(nil), status.TCPPorts...)
	copy.ManagedPorts = append([]int(nil), status.ManagedPorts...)
	copy.StalePorts = append([]int(nil), status.StalePorts...)
	r.lastStatus = &copy
}

func (r *FirewallReconciler) Snapshot() *FirewallStatus {
	if r == nil {
		return nil
	}
	r.statusMu.Lock()
	defer r.statusMu.Unlock()
	if r.lastStatus == nil {
		return nil
	}
	copy := *r.lastStatus
	copy.TCPPorts = append([]int(nil), r.lastStatus.TCPPorts...)
	copy.ManagedPorts = append([]int(nil), r.lastStatus.ManagedPorts...)
	copy.StalePorts = append([]int(nil), r.lastStatus.StalePorts...)
	return &copy
}

func parseUFWStatus(output []byte) string {
	for _, line := range strings.Split(strings.ReplaceAll(string(output), "\r\n", "\n"), "\n") {
		switch strings.TrimSpace(line) {
		case "Status: active":
			return "active"
		case "Status: inactive":
			return "inactive"
		case "":
			continue
		default:
			return "unknown"
		}
	}
	return "unknown"
}

func normalizeLocalFirewallMode(mode string) (string, error) {
	if strings.TrimSpace(mode) == "" {
		return FirewallModeObserve, nil
	}
	return validateFirewallMode(mode)
}

func validateFirewallMode(mode string) (string, error) {
	mode = strings.TrimSpace(mode)
	switch mode {
	case FirewallModeOff, FirewallModeObserve, FirewallModeApply:
		return mode, nil
	default:
		return "", errors.New("invalid firewall mode")
	}
}

func lowerFirewallMode(local, requested string) string {
	rank := map[string]int{
		FirewallModeOff:     0,
		FirewallModeObserve: 1,
		FirewallModeApply:   2,
	}
	if rank[requested] < rank[local] {
		return requested
	}
	return local
}

func validateFirewallTCPPorts(input []int) ([]int, error) {
	if len(input) > maxFirewallTCPPorts {
		return nil, errors.New("firewall assignment exceeds TCP port limit")
	}
	seen := make(map[int]struct{}, len(input))
	ports := make([]int, 0, len(input))
	for _, port := range input {
		if port < 1 || port > 65535 {
			return nil, errors.New("invalid firewall TCP port")
		}
		if _, exists := seen[port]; exists {
			continue
		}
		seen[port] = struct{}{}
		ports = append(ports, port)
	}
	sort.Ints(ports)
	return ports, nil
}

func sanitizedRunnerError(_ []byte, err error) error {
	// Runner output may contain local paths, addresses, or command details. The
	// caller only needs the command failure class; keep raw output in local logs
	// if a concrete Runner implementation chooses to log it.
	if err == nil {
		return errors.New("command failed")
	}
	return err
}
