package agent

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultProcessNameLimit = 8

type Stats struct {
	OS                    string               `json:"os"`
	Arch                  string               `json:"arch"`
	CPUCount              int                  `json:"cpu_count"`
	CPUPercent            *float64             `json:"cpu_percent,omitempty"`
	Load                  []float64            `json:"load"`
	MemoryTotal           uint64               `json:"memory_total_bytes"`
	MemoryAvailable       uint64               `json:"memory_available_bytes"`
	Network               map[string]uint64    `json:"network_bytes"`
	NetworkRates          map[string]float64   `json:"network_bytes_per_second,omitempty"`
	UptimeSeconds         uint64               `json:"uptime_seconds"`
	ProcessCount          *int                 `json:"process_count,omitempty"`
	ProcessNames          []string             `json:"process_names,omitempty"`
	HAProxyVersion        string               `json:"haproxy_version,omitempty"`
	HAProxyStatsAvailable bool                 `json:"haproxy_stats_available"`
	HAProxyRuntime        *HAProxyRuntimeStats `json:"haproxy_runtime,omitempty"`
	QuotaRuntime          map[string]bool      `json:"quota_runtime,omitempty"`
	Firewall              *FirewallStatus      `json:"firewall,omitempty"`
	UpdateVerification    *UpdateVerification  `json:"update_verification,omitempty"`
	KernelPipes           *KernelPipes         `json:"kernel_pipes,omitempty"`
	cpuTotal              uint64
	cpuIdle               uint64
}

type CPUUsageSampler struct {
	mu            sync.Mutex
	previousTotal uint64
	previousIdle  uint64
	initialized   bool
}

type NetworkRateSampler struct {
	mu       sync.Mutex
	previous map[string]uint64
	spare    map[string]uint64
	at       time.Time
}

type ProcessSampler struct {
	ProcRoot        string
	RefreshInterval time.Duration
	Limit           int

	mu    sync.Mutex
	names []string
	at    time.Time
}

// Sample calculates aggregate host CPU usage from consecutive /proc/stat
// snapshots. It is intentionally stateful, like NetworkRateSampler, so the
// heartbeat does not need a second read or a delay.
func (s *CPUUsageSampler) Sample(total, idle uint64) *float64 {
	if s == nil || total == 0 || idle > total {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.initialized || total < s.previousTotal || idle < s.previousIdle {
		s.previousTotal, s.previousIdle, s.initialized = total, idle, true
		return nil
	}
	totalDelta := total - s.previousTotal
	idleDelta := idle - s.previousIdle
	s.previousTotal, s.previousIdle = total, idle
	if totalDelta == 0 || idleDelta > totalDelta {
		return nil
	}
	percent := float64(totalDelta-idleDelta) / float64(totalDelta) * 100
	return &percent
}

type HAProxyVersionSampler struct {
	Runner          Runner
	Binary          string
	RefreshInterval time.Duration

	mu      sync.Mutex
	version string
	at      time.Time
}

// Sample avoids spawning `haproxy -v` for every heartbeat while still
// noticing package upgrades without requiring an Agent restart.
func (s *HAProxyVersionSampler) Sample(ctx context.Context, now time.Time) string {
	if s == nil || s.Runner == nil || s.Binary == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	refresh := s.RefreshInterval
	if refresh <= 0 {
		refresh = 10 * time.Minute
	}
	age := now.Sub(s.at)
	if s.version != "" && !s.at.IsZero() && age >= 0 && age < refresh {
		return s.version
	}
	s.version = HAProxyVersion(ctx, s.Runner, s.Binary)
	s.at = now
	return s.version
}

func (s *NetworkRateSampler) Sample(counters map[string]uint64, now time.Time) map[string]float64 {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Reuse the map retired by the previous sample instead of allocating a
	// new copy of the counters on every heartbeat.
	current := s.spare
	if current == nil {
		current = make(map[string]uint64, len(counters))
	} else {
		clear(current)
	}
	for key, value := range counters {
		current[key] = value
	}
	s.spare = s.previous
	seconds := now.Sub(s.at).Seconds()
	if s.at.IsZero() || seconds <= 0 {
		s.previous, s.at = current, now
		return nil
	}
	rates := make(map[string]float64, len(counters))
	for key, value := range counters {
		previous, exists := s.previous[key]
		if !exists || value < previous {
			continue
		}
		rates[key] = float64(value-previous) / seconds
	}
	s.previous, s.at = current, now
	if len(rates) == 0 {
		return nil
	}
	return rates
}

// Sample returns the most common process names from procfs. The result is
// cached because enumerating /proc, while cheap, is unnecessary per heartbeat.
func (s *ProcessSampler) Sample(now time.Time) []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	refresh := s.RefreshInterval
	if refresh < 5*time.Minute {
		refresh = 5 * time.Minute
	}
	if !s.at.IsZero() && (now.Before(s.at) || now.Sub(s.at) < refresh) {
		return append([]string(nil), s.names...)
	}
	root := s.ProcRoot
	if root == "" {
		root = "/proc"
	}
	limit := s.Limit
	if limit <= 0 || limit > defaultProcessNameLimit {
		limit = defaultProcessNameLimit
	}
	names, err := collectProcessNames(root, limit)
	s.at = now
	if err == nil {
		s.names = names
	}
	return append([]string(nil), s.names...)
}

type processNameCount struct {
	name  string
	count int
}

func collectProcessNames(procRoot string, limit int) ([]string, error) {
	if limit <= 0 || limit > defaultProcessNameLimit {
		limit = defaultProcessNameLimit
	}
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int)
	for _, entry := range entries {
		if !entry.IsDir() || !isNumeric(entry.Name()) {
			continue
		}
		processDir := filepath.Join(procRoot, entry.Name())
		raw, readErr := os.ReadFile(filepath.Join(processDir, "cmdline"))
		if readErr != nil || len(raw) == 0 {
			continue
		}
		argv0 := raw
		if separator := bytes.IndexByte(raw, 0); separator >= 0 {
			argv0 = raw[:separator]
		}
		name := filepath.Base(strings.TrimSpace(string(argv0)))
		if name == "" || name == "." || name == string(filepath.Separator) {
			comm, commErr := os.ReadFile(filepath.Join(processDir, "comm"))
			if commErr == nil {
				name = strings.TrimSpace(string(comm))
			}
		}
		if name != "" {
			counts[name]++
		}
	}
	ranked := make([]processNameCount, 0, len(counts))
	for name, count := range counts {
		ranked = append(ranked, processNameCount{name: name, count: count})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].count != ranked[j].count {
			return ranked[i].count > ranked[j].count
		}
		return ranked[i].name < ranked[j].name
	})
	if limit < len(ranked) {
		ranked = ranked[:limit]
	}
	names := make([]string, len(ranked))
	for i := range ranked {
		names[i] = ranked[i].name
	}
	return names, nil
}

func isNumeric(value string) bool {
	if value == "" {
		return false
	}
	for i := range value {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

func CollectStats() (Stats, error) {
	s := Stats{OS: runtime.GOOS, Arch: runtime.GOARCH, CPUCount: runtime.NumCPU(), Network: map[string]uint64{}}
	if uptime, readErr := os.ReadFile("/proc/uptime"); readErr == nil {
		fields := strings.Fields(string(uptime))
		if len(fields) > 0 {
			seconds, parseErr := strconv.ParseFloat(fields[0], 64)
			if parseErr == nil && seconds >= 0 {
				s.UptimeSeconds = uint64(seconds)
			}
		}
	}
	if cpu, readErr := os.ReadFile("/proc/stat"); readErr == nil {
		s.cpuTotal, s.cpuIdle, _ = parseCPUCounters(string(cpu))
	}
	load, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return s, err
	}
	loadFields := strings.Fields(string(load))
	s.ProcessCount = parseProcessCount(loadFields)
	if len(loadFields) > 3 {
		loadFields = loadFields[:3]
	}
	for _, value := range loadFields {
		v, _ := strconv.ParseFloat(value, 64)
		s.Load = append(s.Load, v)
	}
	mem, err := os.Open("/proc/meminfo")
	if err != nil {
		return s, err
	}
	defer mem.Close()
	scanner := bufio.NewScanner(mem)
	for scanner.Scan() {
		if key, kb, ok := parseMeminfoLine(scanner.Bytes()); ok {
			if key == "MemTotal:" {
				s.MemoryTotal = kb * 1024
			}
			if key == "MemAvailable:" {
				s.MemoryAvailable = kb * 1024
			}
		}
	}
	net, err := os.Open("/proc/net/dev")
	if err != nil {
		return s, err
	}
	defer net.Close()
	scanner = bufio.NewScanner(net)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 10 && strings.HasSuffix(fields[0], ":") {
			iface := strings.TrimSuffix(fields[0], ":")
			rx, _ := strconv.ParseUint(fields[1], 10, 64)
			tx, _ := strconv.ParseUint(fields[9], 10, 64)
			s.Network[iface+"_rx"] = rx
			s.Network[iface+"_tx"] = tx
		}
	}
	return s, scanner.Err()
}

// parseMeminfoLine extracts the MemTotal/MemAvailable "<key> <n> kB" lines
// without fmt.Sscanf, which allocated for every /proc/meminfo line. Other keys
// are ignored because CollectStats does not use them.
func parseMeminfoLine(line []byte) (string, uint64, bool) {
	var key string
	switch {
	case bytes.HasPrefix(line, []byte("MemTotal:")):
		key = "MemTotal:"
	case bytes.HasPrefix(line, []byte("MemAvailable:")):
		key = "MemAvailable:"
	default:
		return "", 0, false
	}
	rest := bytes.TrimSpace(line[len(key):])
	value, unit, found := bytes.Cut(rest, []byte{' '})
	if !found || string(bytes.TrimSpace(unit)) != "kB" {
		return "", 0, false
	}
	kb, err := strconv.ParseUint(string(value), 10, 64)
	if err != nil {
		return "", 0, false
	}
	return key, kb, true
}

func parseProcessCount(loadFields []string) *int {
	if len(loadFields) < 4 {
		return nil
	}
	parts := strings.Split(loadFields[3], "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil
	}
	running, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		return nil
	}
	total, err := strconv.Atoi(parts[1])
	if err != nil || total <= 0 || running > uint64(total) {
		return nil
	}
	return &total
}

func parseCPUCounters(raw string) (total, idle uint64, ok bool) {
	// Only the aggregate "cpu" line is needed; walk lines without splitting
	// the whole of /proc/stat, which has one line per CPU.
	for raw != "" {
		line := raw
		if newline := strings.IndexByte(raw, '\n'); newline >= 0 {
			line, raw = raw[:newline], raw[newline+1:]
		} else {
			raw = ""
		}
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] != "cpu" {
			continue
		}
		// Linux exposes user nice system idle iowait irq softirq steal,
		// followed by guest counters already included in user/nice.
		limit := len(fields)
		if limit > 9 {
			limit = 9
		}
		var values [8]uint64
		for i := 1; i < limit; i++ {
			value, err := strconv.ParseUint(fields[i], 10, 64)
			if err != nil {
				return 0, 0, false
			}
			values[i-1] = value
			total += value
		}
		idle = values[3]
		if limit-1 > 4 {
			idle += values[4]
		}
		return total, idle, total > 0 && idle <= total
	}
	return 0, 0, false
}

func HAProxyVersion(ctx context.Context, runner Runner, binary string) string {
	out, err := runner.Run(ctx, binary, "-v")
	if err != nil {
		return "unavailable"
	}
	line := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	fields := strings.Fields(line)
	if len(fields) >= 3 && strings.EqualFold(fields[0], "haproxy") && strings.EqualFold(fields[1], "version") {
		return fields[2]
	}
	return line
}
