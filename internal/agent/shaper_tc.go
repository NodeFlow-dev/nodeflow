package agent

// Kernel per-client shaper.
//
// Routes with shaper_mode=kernel are rendered without HAProxy bwlim filters so
// the frontend keeps the splice(2) fast path. The Panel renderer instead emits
// a structured comment inside the frontend block:
//
//	# nf-kernel-shaper listen=<ip|*> port=<port> download_bps=<n> upload_bps=<n>
//
// The Agent turns these annotations into one nftables table
// (inet nodeflow_shaper) with per-client token buckets held in dynamic sets:
//
//	output: tcp sport <port> update @dN { <client daddr> limit rate over R bytes/second burst B bytes } drop
//	input:  tcp dport <port> update @uN { <client saddr> limit rate over R bytes/second burst B bytes } drop
//
// Design choice (nft meters rather than tc HTB + flower/u32 + ifb):
//   - HTB needs one class (+ leaf qdisc + filter) per client IP. Client IPs are
//     not known in advance, so the Agent would have to watch conntrack and
//     churn thousands of classes; u32/flow hashing onto a fixed class pool
//     either collides clients (inaccurate) or requires a class per bucket.
//     HTB also serialises every packet of the device on one qdisc root lock.
//   - A dynamic nft set creates the per-client bucket lazily on the first
//     packet (O(1) hash lookup, lockless on the fast path, per-element
//     timeout garbage collection), handles IPv4 and IPv6 /64 with no extra
//     state, and applies to ingress in the input hook, so no ifb redirect is
//     required. TCP converges on the policed rate because drops are paced by
//     the token bucket and the sender backs off.
//   - The whole table is replaced atomically in one nft transaction and is
//     entirely owned by NodeFlow, so reconcile is idempotent and removal is a
//     single "delete table".
//
// The desired table carries a sha256 of the generated script in the output
// chain comment. Reconcile compares that marker with the live chain and only
// runs nft when it differs; a missing table with no desired rules is a no-op.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	KernelShaperTable           = "nodeflow_shaper"
	kernelShaperAnnotation      = "# nf-kernel-shaper "
	kernelShaperMarkerPrefix    = "nodeflow-shaper sha256="
	kernelShaperMaxRules        = 1024
	kernelShaperMaxBytesPerSec  = int64(1_000_000) * 125_000 // 1 Tbit/s, matches client_*_mbps upper bound
	kernelShaperSetSize         = 65536
	kernelShaperSetTimeout      = "5m"
	kernelShaperMinBurstBytes   = int64(128 << 10)
	kernelShaperDriftCheckEvery = time.Minute

	KernelShaperModeOff    = "off"
	KernelShaperModeApply  = "apply"
	KernelShaperModeDryRun = "dry-run"

	kernelShaperFailedCode = "kernel_shaper_failed"
)

// KernelShaperRule is one listener that carries per-client limits enforced by
// the kernel. Zero means unlimited in that direction.
type KernelShaperRule struct {
	ListenIP    string // "*" for any address
	Port        int
	DownloadBps int64 // server -> client (egress, output hook)
	UploadBps   int64 // client -> server (ingress, input hook)
}

// ParseKernelShaperRules extracts kernel shaper annotations from a rendered
// HAProxy configuration. Unknown keys or malformed values reject the whole
// configuration so a partial rule set is never applied.
func ParseKernelShaperRules(config []byte) ([]KernelShaperRule, error) {
	var rules []KernelShaperRule
	seen := map[string]int{}
	for lineNo, raw := range strings.Split(string(config), "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, kernelShaperAnnotation) {
			continue
		}
		rule := KernelShaperRule{}
		fields := strings.Fields(strings.TrimPrefix(line, kernelShaperAnnotation))
		have := map[string]bool{}
		for _, field := range fields {
			key, value, ok := strings.Cut(field, "=")
			if !ok || have[key] {
				return nil, fmt.Errorf("line %d: malformed kernel shaper annotation", lineNo+1)
			}
			have[key] = true
			switch key {
			case "listen":
				if value != "*" && net.ParseIP(value) == nil {
					return nil, fmt.Errorf("line %d: invalid listen address", lineNo+1)
				}
				rule.ListenIP = value
			case "port":
				port, err := strconv.Atoi(value)
				if err != nil || port < 1 || port > 65535 {
					return nil, fmt.Errorf("line %d: invalid port", lineNo+1)
				}
				rule.Port = port
			case "download_bps", "upload_bps":
				rate, err := strconv.ParseInt(value, 10, 64)
				if err != nil || rate < 0 || rate > kernelShaperMaxBytesPerSec {
					return nil, fmt.Errorf("line %d: invalid %s", lineNo+1, key)
				}
				if key == "download_bps" {
					rule.DownloadBps = rate
				} else {
					rule.UploadBps = rate
				}
			default:
				return nil, fmt.Errorf("line %d: unknown kernel shaper key %q", lineNo+1, key)
			}
		}
		if !have["listen"] || !have["port"] {
			return nil, fmt.Errorf("line %d: kernel shaper annotation requires listen and port", lineNo+1)
		}
		if rule.DownloadBps == 0 && rule.UploadBps == 0 {
			continue
		}
		key := rule.ListenIP + "|" + strconv.Itoa(rule.Port)
		if prev, dup := seen[key]; dup {
			if rules[prev] != rule {
				return nil, fmt.Errorf("line %d: conflicting kernel shaper rules for %s", lineNo+1, key)
			}
			continue
		}
		if len(rules) >= kernelShaperMaxRules {
			return nil, fmt.Errorf("kernel shaper rule count exceeds %d", kernelShaperMaxRules)
		}
		seen[key] = len(rules)
		rules = append(rules, rule)
	}
	sort.Slice(rules, func(i, j int) bool {
		if rules[i].Port != rules[j].Port {
			return rules[i].Port < rules[j].Port
		}
		return rules[i].ListenIP < rules[j].ListenIP
	})
	return rules, nil
}

// kernelShaperBurst sizes the token bucket: 50 ms of traffic, at least 128 KiB
// so a single TSO/GRO super-packet (up to 64 KiB) never exceeds the bucket.
func kernelShaperBurst(rate int64) int64 {
	burst := rate / 20
	if burst < kernelShaperMinBurstBytes {
		burst = kernelShaperMinBurstBytes
	}
	return burst
}

type kernelShaperFamily struct {
	suffix   string
	nfproto  string
	addr     string // nft payload prefix: ip / ip6
	setType  string
	clientBy func(field string) string
}

var kernelShaperFamilies = []kernelShaperFamily{
	{suffix: "4", nfproto: "ipv4", addr: "ip", setType: "ipv4_addr", clientBy: func(field string) string { return "ip " + field }},
	// IPv6 clients are keyed per /64, matching the HAProxy bwlim key ipmask(32,64).
	{suffix: "6", nfproto: "ipv6", addr: "ip6", setType: "ipv6_addr", clientBy: func(field string) string { return "ip6 " + field + " & ffff:ffff:ffff:ffff::" }},
}

func (f kernelShaperFamily) matches(listenIP string) bool {
	switch listenIP {
	case "*", "::":
		return true
	case "0.0.0.0":
		return f.suffix == "4"
	}
	ip := net.ParseIP(listenIP)
	if ip == nil {
		return false
	}
	if ip.To4() != nil {
		return f.suffix == "4"
	}
	return f.suffix == "6"
}

func (f kernelShaperFamily) localMatch(listenIP, field string) string {
	switch listenIP {
	case "*", "::", "0.0.0.0":
		return ""
	}
	ip := net.ParseIP(listenIP)
	if ip4 := ip.To4(); ip4 != nil {
		return f.addr + " " + field + " " + ip4.String() + " "
	}
	return f.addr + " " + field + " " + ip.String() + " "
}

// BuildKernelShaperScript renders the complete nft transaction for rules and
// returns it with its content hash. The script is atomic: it creates the table
// (so delete never fails), deletes it, and recreates it in one transaction.
// With no rules the returned script is empty.
func BuildKernelShaperScript(rules []KernelShaperRule) (string, string) {
	if len(rules) == 0 {
		return "", ""
	}
	var sets, output, input strings.Builder
	for i, rule := range rules {
		for _, fam := range kernelShaperFamilies {
			if !fam.matches(rule.ListenIP) {
				continue
			}
			port := strconv.Itoa(rule.Port)
			if rule.DownloadBps > 0 {
				set := fmt.Sprintf("d%s_%d", fam.suffix, i)
				fmt.Fprintf(&sets, "\tset %s { type %s; size %d; flags dynamic,timeout; timeout %s; }\n", set, fam.setType, kernelShaperSetSize, kernelShaperSetTimeout)
				fmt.Fprintf(&output, "\t\tmeta nfproto %s %stcp sport %s update @%s { %s limit rate over %d bytes/second burst %d bytes } drop\n",
					fam.nfproto, fam.localMatch(rule.ListenIP, "saddr"), port, set, fam.clientBy("daddr"), rule.DownloadBps, kernelShaperBurst(rule.DownloadBps))
			}
			if rule.UploadBps > 0 {
				set := fmt.Sprintf("u%s_%d", fam.suffix, i)
				fmt.Fprintf(&sets, "\tset %s { type %s; size %d; flags dynamic,timeout; timeout %s; }\n", set, fam.setType, kernelShaperSetSize, kernelShaperSetTimeout)
				fmt.Fprintf(&input, "\t\tmeta nfproto %s %stcp dport %s update @%s { %s limit rate over %d bytes/second burst %d bytes } drop\n",
					fam.nfproto, fam.localMatch(rule.ListenIP, "daddr"), port, set, fam.clientBy("saddr"), rule.UploadBps, kernelShaperBurst(rule.UploadBps))
			}
		}
	}
	body := func(marker string) string {
		var b strings.Builder
		b.WriteString("table inet " + KernelShaperTable + "\n")
		b.WriteString("delete table inet " + KernelShaperTable + "\n")
		b.WriteString("table inet " + KernelShaperTable + " {\n")
		b.WriteString(sets.String())
		b.WriteString("\tchain output {\n")
		b.WriteString("\t\ttype filter hook output priority filter; policy accept;\n")
		b.WriteString("\t\tcomment \"" + kernelShaperMarkerPrefix + marker + "\"\n")
		// Loopback carries HAProxy <-> local backend legs, never client traffic.
		b.WriteString("\t\toifname \"lo\" accept\n")
		b.WriteString(output.String())
		b.WriteString("\t}\n")
		b.WriteString("\tchain input {\n")
		b.WriteString("\t\ttype filter hook input priority filter; policy accept;\n")
		b.WriteString("\t\tiifname \"lo\" accept\n")
		b.WriteString(input.String())
		b.WriteString("\t}\n")
		b.WriteString("}\n")
		return b.String()
	}
	sum := sha256.Sum256([]byte(body("")))
	hash := hex.EncodeToString(sum[:])
	return body(hash), hash
}

// KernelShaperStatus is the last reconcile outcome.
type KernelShaperStatus struct {
	Mode         string    `json:"mode"`
	Rules        int       `json:"rules"`
	Hash         string    `json:"hash,omitempty"`
	Changed      bool      `json:"changed"`
	Removed      bool      `json:"removed"`
	Planned      string    `json:"planned,omitempty"` // dry-run only
	LastError    string    `json:"last_error,omitempty"`
	ReconciledAt time.Time `json:"reconciled_at"`
}

type configReportPoster interface {
	Post(context.Context, ConfigReport) error
}

// KernelShaper reconciles the nodeflow_shaper nftables table with the kernel
// shaper annotations of the active managed HAProxy configuration.
type KernelShaper struct {
	Runner   Runner
	Manager  *ConfigManager
	Reporter configReportPoster
	Mode     string // off | apply | dry-run
	Binary   string // nft binary, default "nft"
	TempDir  string
	Logger   *log.Logger
	Now      func() time.Time

	mu            sync.Mutex
	lastConfigSum [32]byte
	lastOK        bool
	lastCheck     time.Time
	reportedFail  string // "<revision>|<reason>" of the last failure reported
	failStreak    int
	lastPlanned   string
	status        KernelShaperStatus
}

// NewKernelShaperFromEnv builds a shaper from NODE_AGENT_KERNEL_SHAPER
// (apply|dry-run|off, default apply) and NODE_AGENT_NFT_BINARY.
func NewKernelShaperFromEnv(runner Runner, manager *ConfigManager, reporter configReportPoster) *KernelShaper {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("NODE_AGENT_KERNEL_SHAPER")))
	switch mode {
	case "", "auto", "on", "apply":
		mode = KernelShaperModeApply
	case "dry-run", "dryrun", "dry_run":
		mode = KernelShaperModeDryRun
	default:
		mode = KernelShaperModeOff
	}
	binary := strings.TrimSpace(os.Getenv("NODE_AGENT_NFT_BINARY"))
	if binary == "" {
		binary = "nft"
	}
	return &KernelShaper{Runner: runner, Manager: manager, Reporter: reporter, Mode: mode, Binary: binary}
}

func (s *KernelShaper) Snapshot() KernelShaperStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *KernelShaper) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *KernelShaper) binary() string {
	if s.Binary == "" {
		return "nft"
	}
	return s.Binary
}

// managedConfigSnapshot reads the active config and its revision marker under
// the manager lock, so both always describe the same HAProxy generation.
func (m *ConfigManager) managedConfigSnapshot() ([]byte, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	config, err := os.ReadFile(m.ManagedConfig)
	if err != nil && !os.IsNotExist(err) {
		return nil, 0, err
	}
	var revision int64
	if marker, markerErr := m.actualRevision(); markerErr == nil {
		revision, _ = strconv.ParseInt(strings.TrimSpace(marker), 10, 64)
	}
	return config, revision, nil
}

// liveHash returns the marker of the installed table, "" when absent.
func (s *KernelShaper) liveHash(ctx context.Context) (string, bool, error) {
	out, err := s.Runner.Run(ctx, s.binary(), "list", "chain", "inet", KernelShaperTable, "output")
	if err != nil {
		if strings.Contains(string(out), "No such file or directory") {
			return "", false, nil
		}
		return "", false, fmt.Errorf("nft list: %s", firstLine(out, err))
	}
	text := string(out)
	idx := strings.Index(text, kernelShaperMarkerPrefix)
	if idx < 0 {
		return "", true, nil
	}
	rest := text[idx+len(kernelShaperMarkerPrefix):]
	if end := strings.IndexAny(rest, "\" \n"); end >= 0 {
		rest = rest[:end]
	}
	return rest, true, nil
}

func firstLine(out []byte, err error) string {
	text := strings.TrimSpace(string(out))
	if text == "" && err != nil {
		text = err.Error()
	}
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	if len(text) > 200 {
		text = text[:200]
	}
	return text
}

func (s *KernelShaper) runScript(ctx context.Context, script string, check bool) error {
	file, err := os.CreateTemp(s.TempDir, "nodeflow-shaper-*.nft")
	if err != nil {
		return fmt.Errorf("create nft script: %w", err)
	}
	path := file.Name()
	defer os.Remove(path)
	if _, err = file.WriteString(script); err != nil {
		file.Close()
		return fmt.Errorf("write nft script: %w", err)
	}
	if err = file.Close(); err != nil {
		return fmt.Errorf("write nft script: %w", err)
	}
	args := []string{"-f", path}
	if check {
		args = []string{"-c", "-f", path}
	}
	if out, runErr := s.Runner.Run(ctx, s.binary(), args...); runErr != nil {
		return fmt.Errorf("nft -f: %s", firstLine(out, runErr))
	}
	return nil
}

// Reconcile brings the kernel state in line with the active managed config.
// It is cheap when nothing changed: the config hash is compared in memory and
// nft is consulted only for drift detection once per minute.
func (s *KernelShaper) Reconcile(ctx context.Context) (KernelShaperStatus, error) {
	if s == nil || s.Runner == nil || s.Manager == nil {
		return KernelShaperStatus{}, errors.New("kernel shaper is not configured")
	}
	mode := s.Mode
	if mode == "" {
		mode = KernelShaperModeApply
	}
	status := KernelShaperStatus{Mode: mode, ReconciledAt: s.now()}
	if mode == KernelShaperModeOff {
		s.setStatus(status)
		return status, nil
	}
	config, revision, err := s.Manager.managedConfigSnapshot()
	if err != nil {
		return s.fail(ctx, status, revision, fmt.Errorf("read managed config: %w", err))
	}
	sum := sha256.Sum256(config)
	s.mu.Lock()
	// With no shaped routes and no table installed there is nothing that can
	// drift, so the periodic `nft list` (a process spawn every minute on every
	// node) is skipped until the managed config changes.
	idle := s.lastOK && sum == s.lastConfigSum && s.status.Rules == 0 && !s.status.Removed && s.status.Hash == ""
	unchanged := s.lastOK && sum == s.lastConfigSum && (idle || s.now().Sub(s.lastCheck) < kernelShaperDriftCheckEvery)
	s.mu.Unlock()
	if unchanged {
		return s.Snapshot(), nil
	}

	rules, err := ParseKernelShaperRules(config)
	if err != nil {
		return s.fail(ctx, status, revision, err)
	}
	script, hash := BuildKernelShaperScript(rules)
	status.Rules, status.Hash = len(rules), hash

	live, present, err := s.liveHash(ctx)
	if err != nil {
		if len(rules) == 0 && isExecNotFound(err) {
			// No nft installed and nothing to enforce: nothing to remove either.
			s.succeed(ctx, status, revision, sum)
			return status, nil
		}
		return s.fail(ctx, status, revision, err)
	}
	switch {
	case len(rules) == 0 && !present:
		// Nothing desired, nothing installed.
	case len(rules) == 0 && present:
		status.Removed = true
		if mode == KernelShaperModeDryRun {
			status.Planned = "delete table inet " + KernelShaperTable
		} else if out, runErr := s.Runner.Run(ctx, s.binary(), "delete", "table", "inet", KernelShaperTable); runErr != nil {
			return s.fail(ctx, status, revision, fmt.Errorf("nft delete table: %s", firstLine(out, runErr)))
		}
	case present && live == hash:
		// Idempotent: installed table already matches.
	default:
		status.Changed = true
		if mode == KernelShaperModeDryRun {
			status.Planned = script
			if err := s.runScript(ctx, script, true); err != nil {
				return s.fail(ctx, status, revision, err)
			}
		} else if err := s.runScript(ctx, script, false); err != nil {
			return s.fail(ctx, status, revision, err)
		}
	}
	s.succeed(ctx, status, revision, sum)
	return status, nil
}

func isExecNotFound(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "executable file not found") || strings.Contains(msg, "no such file or directory")
}

func (s *KernelShaper) setStatus(status KernelShaperStatus) {
	s.mu.Lock()
	s.status = status
	s.mu.Unlock()
}

func (s *KernelShaper) succeed(ctx context.Context, status KernelShaperStatus, revision int64, sum [32]byte) {
	s.mu.Lock()
	s.status = status
	s.lastOK = true
	s.lastConfigSum = sum
	s.lastCheck = s.now()
	s.failStreak = 0
	reported := s.reportedFail
	s.reportedFail = ""
	s.mu.Unlock()
	// A failure was surfaced for this revision; tell the Panel it recovered.
	if reported != "" && revision > 0 && s.Reporter != nil &&
		strings.HasPrefix(reported, strconv.FormatInt(revision, 10)+"|") {
		rev := revision
		_ = s.Reporter.Post(ctx, ConfigReport{
			Revision: revision, State: "applied", ActualRevision: &rev,
			Details: map[string]any{"component": "kernel_shaper", "recovered": true, "rules": status.Rules},
		})
	}
}

func (s *KernelShaper) fail(ctx context.Context, status KernelShaperStatus, revision int64, err error) (KernelShaperStatus, error) {
	status.LastError = err.Error()
	key := strconv.FormatInt(revision, 10) + "|" + status.LastError
	s.mu.Lock()
	s.status = status
	s.lastOK = false
	s.failStreak++
	already := s.reportedFail == key
	// The config Reconciler posts "applied" for a new revision concurrently
	// with the first shaper pass. Only report a failure that survived a
	// second pass so the Panel never sees failed-then-applied reordering.
	persistent := s.failStreak >= 2
	s.mu.Unlock()
	// Report once per (revision, error) through the config-report path so the
	// Panel shows node config state failed/kernel_shaper_failed.
	if !already && persistent && revision > 0 && s.Reporter != nil {
		rev := revision
		postErr := s.Reporter.Post(ctx, ConfigReport{
			Revision: revision, State: "failed", ActualRevision: &rev, Error: kernelShaperFailedCode,
			Details: map[string]any{"reason": kernelShaperFailedCode, "component": "kernel_shaper", "mode": status.Mode, "message": status.LastError},
		})
		if postErr == nil {
			s.mu.Lock()
			s.reportedFail = key
			s.mu.Unlock()
		}
	}
	return status, err
}

// Run reconciles periodically until ctx is cancelled. Errors are logged once
// per distinct message.
func (s *KernelShaper) Run(ctx context.Context, interval time.Duration) {
	if s == nil || s.Mode == KernelShaperModeOff {
		return
	}
	if interval <= 0 {
		interval = 5 * time.Second
	}
	logger := s.Logger
	if logger == nil {
		logger = log.Default()
	}
	lastErr := ""
	tick := func() {
		runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		status, err := s.Reconcile(runCtx)
		cancel()
		if err != nil {
			if msg := err.Error(); msg != lastErr && ctx.Err() == nil {
				logger.Printf("kernel shaper reconciliation failed: %s", msg)
				lastErr = msg
			}
			return
		}
		if lastErr != "" {
			logger.Printf("kernel shaper reconciliation recovered")
			lastErr = ""
		}
		switch {
		case status.Planned != "" && status.Planned != s.lastPlanned:
			s.lastPlanned = status.Planned
			logger.Printf("kernel shaper dry-run: would apply %d rule(s):\n%s", status.Rules, status.Planned)
		case status.Removed:
			logger.Printf("kernel shaper removed table inet %s", KernelShaperTable)
		case status.Changed:
			logger.Printf("kernel shaper applied %d rule(s) sha256=%s", status.Rules, status.Hash)
		}
	}
	tick()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick()
		}
	}
}
