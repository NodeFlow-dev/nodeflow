package agent

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultHAProxyStatsTimeout = 2 * time.Second
	defaultHAProxyMaxResponse  = 8 << 20
	maxHAProxyMetricNameBytes  = 128
	maxHAProxyMetricTextBytes  = 256
	maxHAProxyFrontendMetrics  = 1024
	maxHAProxyBackendMetrics   = 1024
	maxHAProxyServerMetrics    = 1024
)

// HAProxyRuntimeCollector is intentionally small so heartbeat degradation can
// be tested without a real HAProxy process.
type HAProxyRuntimeCollector interface {
	Collect(context.Context) (HAProxyRuntimeStats, error)
}

// HAProxyRuntimeStats is the stable, typed subset of the HAProxy CLI exposed to
// the panel. Raw CLI output is never retained or sent.
type HAProxyRuntimeStats struct {
	CounterGeneration  string                                   `json:"counter_generation,omitempty"`
	ConnectionsCurrent uint64                                   `json:"connections_current"`
	ConnectionsTotal   uint64                                   `json:"connections_total"`
	ConnectionRate     uint64                                   `json:"connection_rate"`
	BytesIn            uint64                                   `json:"bytes_in"`
	BytesOut           uint64                                   `json:"bytes_out"`
	Frontends          map[string]HAProxyProxyStats             `json:"frontends"`
	Backends           map[string]HAProxyProxyStats             `json:"backends"`
	Servers            map[string]map[string]HAProxyServerStats `json:"servers"`
	Truncated          bool                                     `json:"truncated,omitempty"`
}

type HAProxyProxyStats struct {
	Status          string `json:"status,omitempty"`
	SessionsCurrent uint64 `json:"sessions_current"`
	SessionsTotal   uint64 `json:"sessions_total"`
	SessionRate     uint64 `json:"session_rate"`
	SessionLimit    uint64 `json:"session_limit"`
	QueueCurrent    uint64 `json:"queue_current"`
	QueueMax        uint64 `json:"queue_max"`
	BytesIn         uint64 `json:"bytes_in"`
	BytesOut        uint64 `json:"bytes_out"`
}

type HAProxyServerStats struct {
	Address           string `json:"address"`
	Status            string `json:"status,omitempty"`
	CheckStatus       string `json:"check_status,omitempty"`
	CheckCode         uint64 `json:"check_code,omitempty"`
	CheckDurationMS   uint64 `json:"check_duration_ms,omitempty"`
	CheckDescription  string `json:"check_description,omitempty"`
	SessionsCurrent   uint64 `json:"sessions_current"`
	SessionsTotal     uint64 `json:"sessions_total"`
	SessionRate       uint64 `json:"session_rate"`
	QueueCurrent      uint64 `json:"queue_current"`
	QueueMax          uint64 `json:"queue_max"`
	BytesIn           uint64 `json:"bytes_in"`
	BytesOut          uint64 `json:"bytes_out"`
	LastChangeSeconds uint64 `json:"last_change_seconds,omitempty"`
	DowntimeSeconds   uint64 `json:"downtime_seconds,omitempty"`
	Weight            uint64 `json:"weight,omitempty"`
	Active            bool   `json:"active"`
	Backup            bool   `json:"backup"`
}

// HAProxySocketClient reads the read-only HAProxy runtime CLI over a Unix
// socket. Timeout is a budget for the complete info+stat collection, not for
// each individual command.
type HAProxySocketClient struct {
	Path             string
	Timeout          time.Duration
	MaxResponseBytes int64
}

func (c HAProxySocketClient) Collect(ctx context.Context) (HAProxyRuntimeStats, error) {
	stats := emptyHAProxyRuntimeStats()
	if err := validateHAProxySocketPath(c.Path); err != nil {
		return stats, err
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = defaultHAProxyStatsTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	infoResponse, infoQueryErr, statResponse, statQueryErr := c.queryInfoAndStat(ctx)
	infoOK := false
	statOK := false
	var info showInfoMetrics
	if infoQueryErr == nil {
		var err error
		info, err = parseHAProxyShowInfo(infoResponse)
		infoOK = err == nil
	}
	if statQueryErr == nil {
		parsed, err := parseHAProxyShowStat(statResponse)
		if err == nil {
			stats = parsed
			statOK = true
		}
	}
	// Traffic accounting is derived from show stat cumulative counters. A
	// successful show info response must never turn a failed/invalid show stat
	// response into an all-zero snapshot: Panel would interpret that as a
	// counter reset and charge the next healthy snapshot twice.
	if !statOK {
		return emptyHAProxyRuntimeStats(), errors.New("HAProxy runtime stats unavailable")
	}
	if infoOK {
		stats.CounterGeneration = info.counterGeneration()
		if info.hasConnectionsCurrent {
			stats.ConnectionsCurrent = info.connectionsCurrent
		}
		if info.hasConnectionsTotal {
			stats.ConnectionsTotal = info.connectionsTotal
		}
		if info.hasConnectionRate {
			stats.ConnectionRate = info.connectionRate
		}
	}
	return stats, nil
}

func (c HAProxySocketClient) query(ctx context.Context, command string) ([]byte, error) {
	if command != "show info" && command != "show stat" {
		return nil, errors.New("unsupported HAProxy CLI command")
	}
	return c.queryRaw(ctx, command)
}

// showStatHeaderPrefix starts every "show stat" CSV response and never occurs
// in "show info" output, which only contains "Key: value" lines.
const showStatHeaderPrefix = "# pxname,"

var errHAProxyResponseTooLarge = errors.New("HAProxy stats response exceeds limit")

// queryInfoAndStat sends "show info;show stat" over a single CLI connection.
// HAProxy executes ';'-separated commands in order on one connection, so the
// combined response is the info block followed by the stat CSV, which always
// starts with its "# pxname," header line. If that header cannot be located
// (for example an older or restricted CLI rejected the compound command), it
// falls back to one connection per command, preserving the previous behavior.
func (c HAProxySocketClient) queryInfoAndStat(ctx context.Context) (info []byte, infoErr error, stat []byte, statErr error) {
	maxResponse := c.maxResponseBytes()
	combined, err := c.queryRawLimit(ctx, "show info;show stat", 2*maxResponse)
	if err != nil && !errors.Is(err, errHAProxyResponseTooLarge) {
		// Transport failures affect both commands equally; retrying them
		// separately would only spend more of the shared timeout budget.
		return nil, err, nil, err
	}
	if err == nil {
		if info, stat, ok := splitInfoAndStat(combined); ok {
			if int64(len(info)) > maxResponse {
				info, infoErr = nil, errHAProxyResponseTooLarge
			}
			if int64(len(stat)) > maxResponse {
				stat, statErr = nil, errHAProxyResponseTooLarge
			}
			return info, infoErr, stat, statErr
		}
	}
	info, infoErr = c.query(ctx, "show info")
	stat, statErr = c.query(ctx, "show stat")
	return info, infoErr, stat, statErr
}

func splitInfoAndStat(response []byte) (info, stat []byte, ok bool) {
	if bytes.HasPrefix(response, []byte(showStatHeaderPrefix)) {
		return nil, response, true
	}
	index := bytes.Index(response, []byte("\n"+showStatHeaderPrefix))
	if index < 0 {
		return nil, nil, false
	}
	return response[:index+1], response[index+1:], true
}

func (c HAProxySocketClient) maxResponseBytes() int64 {
	if c.MaxResponseBytes <= 0 {
		return defaultHAProxyMaxResponse
	}
	return c.MaxResponseBytes
}

func (c HAProxySocketClient) queryRaw(ctx context.Context, command string) ([]byte, error) {
	return c.queryRawLimit(ctx, command, c.maxResponseBytes())
}

func (c HAProxySocketClient) queryRawLimit(ctx context.Context, command string, maxResponse int64) ([]byte, error) {
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "unix", c.Path)
	if err != nil {
		return nil, errors.New("HAProxy stats socket unavailable")
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return nil, errors.New("cannot set HAProxy stats socket deadline")
		}
	}
	if _, err := io.WriteString(conn, command+"\n"); err != nil {
		return nil, errors.New("cannot write HAProxy stats command")
	}
	if closeWriter, ok := conn.(interface{ CloseWrite() error }); ok {
		if err := closeWriter.CloseWrite(); err != nil {
			return nil, errors.New("cannot finish HAProxy stats command")
		}
	}
	response, err := io.ReadAll(io.LimitReader(conn, maxResponse+1))
	if err != nil {
		return nil, errors.New("cannot read HAProxy stats response")
	}
	if int64(len(response)) > maxResponse {
		return nil, errHAProxyResponseTooLarge
	}
	return response, nil
}

func (c HAProxySocketClient) SetServerMaintenance(ctx context.Context, backend, server string, maintenance bool) error {
	if err := validateHAProxySocketPath(c.Path); err != nil {
		return err
	}
	if !validRuntimeObjectName(backend) || !validRuntimeObjectName(server) {
		return errors.New("invalid HAProxy runtime object name")
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = defaultHAProxyStatsTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	verb := "enable"
	if maintenance {
		verb = "disable"
	}
	response, err := c.queryRaw(ctx, verb+" server "+backend+"/"+server)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(response)) != "" {
		return errors.New("HAProxy rejected runtime server state change")
	}
	return nil
}

// ShowServersState returns the raw "show servers state <backend>" response.
func (c HAProxySocketClient) ShowServersState(ctx context.Context, backend string) ([]byte, error) {
	if err := validateHAProxySocketPath(c.Path); err != nil {
		return nil, err
	}
	if !validRuntimeObjectName(backend) {
		return nil, errors.New("invalid HAProxy runtime object name")
	}
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	return c.queryRaw(ctx, "show servers state "+backend)
}

// ShowStat returns the raw "show stat" CSV response.
func (c HAProxySocketClient) ShowStat(ctx context.Context) ([]byte, error) {
	if err := validateHAProxySocketPath(c.Path); err != nil {
		return nil, err
	}
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	return c.query(ctx, "show stat")
}

// SetServerWeight sets the runtime user weight of one server.
func (c HAProxySocketClient) SetServerWeight(ctx context.Context, backend, server string, weight int) error {
	if err := validateHAProxySocketPath(c.Path); err != nil {
		return err
	}
	if !validRuntimeObjectName(backend) || !validRuntimeObjectName(server) {
		return errors.New("invalid HAProxy runtime object name")
	}
	if weight < 0 || weight > 256 {
		return errors.New("invalid HAProxy server weight")
	}
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	response, err := c.queryRaw(ctx, "set server "+backend+"/"+server+" weight "+strconv.Itoa(weight))
	if err != nil {
		return err
	}
	if text := strings.TrimSpace(string(response)); text != "" {
		return errors.New("HAProxy rejected runtime weight change: " + metricText(text))
	}
	return nil
}

func (c HAProxySocketClient) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = defaultHAProxyStatsTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

func validRuntimeObjectName(value string) bool {
	if value == "" || len(value) > maxHAProxyMetricNameBytes {
		return false
	}
	for _, char := range value {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '-' || char == '.' || char == ':') {
			return false
		}
	}
	return true
}

func validateHAProxySocketPath(path string) error {
	if path == "" || !filepath.IsAbs(path) || strings.ContainsRune(path, '\x00') || len(path) > 107 {
		return errors.New("invalid HAProxy stats socket path")
	}
	return nil
}

type showInfoMetrics struct {
	pid                   uint64
	startTimeSeconds      uint64
	reloads               uint64
	connectionsCurrent    uint64
	connectionsTotal      uint64
	connectionRate        uint64
	hasPID                bool
	hasStartTimeSeconds   bool
	hasReloads            bool
	hasConnectionsCurrent bool
	hasConnectionsTotal   bool
	hasConnectionRate     bool
}

func parseHAProxyShowInfo(response []byte) (showInfoMetrics, error) {
	var result showInfoMetrics
	for _, line := range bytes.Split(response, []byte{'\n'}) {
		keyBytes, valueBytes, ok := bytes.Cut(line, []byte{':'})
		if !ok {
			continue
		}
		key := strings.TrimSpace(string(keyBytes))
		var target *uint64
		var present *bool
		switch key {
		case "Pid":
			target, present = &result.pid, &result.hasPID
		case "Start_time_sec":
			target, present = &result.startTimeSeconds, &result.hasStartTimeSeconds
		case "Reloads":
			target, present = &result.reloads, &result.hasReloads
		case "CurrConns":
			target, present = &result.connectionsCurrent, &result.hasConnectionsCurrent
		case "CumConns":
			target, present = &result.connectionsTotal, &result.hasConnectionsTotal
		case "ConnRate":
			target, present = &result.connectionRate, &result.hasConnectionRate
		default:
			continue
		}
		value, err := strconv.ParseUint(strings.TrimSpace(string(valueBytes)), 10, 64)
		if err != nil {
			return showInfoMetrics{}, fmt.Errorf("invalid HAProxy show info field %s", key)
		}
		*target = value
		*present = true
	}
	if !result.hasConnectionsCurrent && !result.hasConnectionsTotal && !result.hasConnectionRate {
		return showInfoMetrics{}, errors.New("HAProxy show info has no connection metrics")
	}
	return result, nil
}

func (m showInfoMetrics) counterGeneration() string {
	if !m.hasPID || !m.hasStartTimeSeconds {
		return ""
	}
	return strconv.FormatUint(m.pid, 10) + ":" +
		strconv.FormatUint(m.startTimeSeconds, 10) + ":" +
		strconv.FormatUint(m.reloads, 10)
}

// showStatColumns holds the position of every consumed "show stat" column,
// resolved once from the CSV header. Missing columns are -1. Resolving names
// up front keeps the per-row hot path free of map lookups.
type showStatColumns struct {
	pxname, svname, objectType, status             int
	scur, stot, rate, slim, qcur, qmax, bin, bout  int
	addr, checkStatus, checkCode, checkDuration    int
	checkDesc, lastchg, downtime, weight, act, bck int
}

func newShowStatColumns(header []string) (showStatColumns, bool, bool) {
	columns := showStatColumns{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
	for i, name := range header {
		name = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(name), "#"))
		var target *int
		switch name {
		case "pxname":
			target = &columns.pxname
		case "svname":
			target = &columns.svname
		case "type":
			target = &columns.objectType
		case "status":
			target = &columns.status
		case "scur":
			target = &columns.scur
		case "stot":
			target = &columns.stot
		case "rate":
			target = &columns.rate
		case "slim":
			target = &columns.slim
		case "qcur":
			target = &columns.qcur
		case "qmax":
			target = &columns.qmax
		case "bin":
			target = &columns.bin
		case "bout":
			target = &columns.bout
		case "addr":
			target = &columns.addr
		case "check_status":
			target = &columns.checkStatus
		case "check_code":
			target = &columns.checkCode
		case "check_duration":
			target = &columns.checkDuration
		case "check_desc":
			target = &columns.checkDesc
		case "lastchg":
			target = &columns.lastchg
		case "downtime":
			target = &columns.downtime
		case "weight":
			target = &columns.weight
		case "act":
			target = &columns.act
		case "bck":
			target = &columns.bck
		default:
			continue
		}
		// Later duplicates win, matching the previous name->index map.
		*target = i
	}
	return columns, columns.pxname >= 0, columns.svname >= 0
}

// showStatRecords yields the CSV records of a "show stat" response. HAProxy
// never quotes fields, so quote-free responses are split directly on commas
// without encoding/csv; anything containing a quote keeps the lenient
// encoding/csv parser. Both paths yield identical records for quote-free input:
// "\n" and "\r\n" terminators, a trailing "\r" at EOF, and skipped blank
// lines. The yielded slice is reused between calls; its strings are not.
func showStatRecords(response []byte, yield func([]string) error) error {
	if bytes.IndexByte(response, '"') >= 0 {
		reader := csv.NewReader(bytes.NewReader(response))
		reader.FieldsPerRecord = -1
		reader.LazyQuotes = true
		reader.ReuseRecord = true
		for {
			record, err := reader.Read()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return errors.New("invalid HAProxy show stat CSV")
			}
			if err := yield(record); err != nil {
				return err
			}
		}
	}
	// Convert one line at a time, like encoding/csv does per record, so the
	// retained metric names only pin their own row, never the whole response.
	remaining := response
	var record []string
	for len(remaining) > 0 {
		lineBytes := remaining
		if newline := bytes.IndexByte(remaining, '\n'); newline >= 0 {
			lineBytes, remaining = remaining[:newline], remaining[newline+1:]
		} else {
			remaining = nil
		}
		lineBytes = bytes.TrimSuffix(lineBytes, []byte{'\r'})
		if len(lineBytes) == 0 {
			continue
		}
		line := string(lineBytes)
		record = record[:0]
		for {
			comma := strings.IndexByte(line, ',')
			if comma < 0 {
				record = append(record, line)
				break
			}
			record = append(record, line[:comma])
			line = line[comma+1:]
		}
		if err := yield(record); err != nil {
			return err
		}
	}
	return nil
}

func parseHAProxyShowStat(response []byte) (HAProxyRuntimeStats, error) {
	stats := emptyHAProxyRuntimeStats()
	var columns showStatColumns
	haveHeader := false
	frontendCount, backendCount, serverCount := 0, 0, 0
	err := showStatRecords(response, func(record []string) error {
		if len(record) == 0 || (len(record) == 1 && strings.TrimSpace(record[0]) == "") {
			return nil
		}
		if !haveHeader {
			var hasProxy, hasService bool
			columns, hasProxy, hasService = newShowStatColumns(record)
			haveHeader = true
			if !hasProxy {
				return errors.New("HAProxy show stat CSV missing pxname")
			}
			if !hasService {
				return errors.New("HAProxy show stat CSV missing svname")
			}
			return nil
		}

		proxyName, proxyOK := metricName(csvField(record, columns.pxname))
		serviceName, serviceOK := metricName(csvField(record, columns.svname))
		if !proxyOK || !serviceOK {
			stats.Truncated = true
			return nil
		}
		objectType := csvField(record, columns.objectType)
		switch {
		case strings.EqualFold(serviceName, "FRONTEND") || objectType == "0":
			row, parseErr := proxyStatsFromCSV(record, &columns)
			if parseErr != nil {
				return parseErr
			}
			stats.BytesIn = saturatingAdd(stats.BytesIn, row.BytesIn)
			stats.BytesOut = saturatingAdd(stats.BytesOut, row.BytesOut)
			stats.ConnectionsCurrent = saturatingAdd(stats.ConnectionsCurrent, row.SessionsCurrent)
			stats.ConnectionsTotal = saturatingAdd(stats.ConnectionsTotal, row.SessionsTotal)
			stats.ConnectionRate = saturatingAdd(stats.ConnectionRate, row.SessionRate)
			if _, exists := stats.Frontends[proxyName]; !exists {
				if frontendCount >= maxHAProxyFrontendMetrics {
					stats.Truncated = true
					return nil
				}
				frontendCount++
			}
			stats.Frontends[proxyName] = row
		case strings.EqualFold(serviceName, "BACKEND") || objectType == "1":
			row, parseErr := proxyStatsFromCSV(record, &columns)
			if parseErr != nil {
				return parseErr
			}
			if _, exists := stats.Backends[proxyName]; !exists {
				if backendCount >= maxHAProxyBackendMetrics {
					stats.Truncated = true
					return nil
				}
				backendCount++
			}
			stats.Backends[proxyName] = row
		case objectType == "2":
			row, parseErr := serverStatsFromCSV(record, &columns)
			if parseErr != nil {
				return parseErr
			}
			servers := stats.Servers[proxyName]
			if servers == nil {
				servers = make(map[string]HAProxyServerStats)
				stats.Servers[proxyName] = servers
			}
			if _, exists := servers[serviceName]; !exists {
				if serverCount >= maxHAProxyServerMetrics {
					stats.Truncated = true
					return nil
				}
				serverCount++
			}
			servers[serviceName] = row
		}
		return nil
	})
	if err != nil {
		return emptyHAProxyRuntimeStats(), err
	}
	if !haveHeader {
		return emptyHAProxyRuntimeStats(), errors.New("empty HAProxy show stat CSV")
	}
	return stats, nil
}

func emptyHAProxyRuntimeStats() HAProxyRuntimeStats {
	return HAProxyRuntimeStats{
		Frontends: make(map[string]HAProxyProxyStats),
		Backends:  make(map[string]HAProxyProxyStats),
		Servers:   make(map[string]map[string]HAProxyServerStats),
	}
}

func proxyStatsFromCSV(record []string, columns *showStatColumns) (HAProxyProxyStats, error) {
	bytesIn, err := csvTrafficCounter(record, columns.bin, "bin")
	if err != nil {
		return HAProxyProxyStats{}, err
	}
	bytesOut, err := csvTrafficCounter(record, columns.bout, "bout")
	if err != nil {
		return HAProxyProxyStats{}, err
	}
	return HAProxyProxyStats{
		Status:          metricText(csvField(record, columns.status)),
		SessionsCurrent: csvUint(record, columns.scur),
		SessionsTotal:   csvUint(record, columns.stot),
		SessionRate:     csvUint(record, columns.rate),
		SessionLimit:    csvUint(record, columns.slim),
		QueueCurrent:    csvUint(record, columns.qcur),
		QueueMax:        csvUint(record, columns.qmax),
		BytesIn:         bytesIn,
		BytesOut:        bytesOut,
	}, nil
}

func serverStatsFromCSV(record []string, columns *showStatColumns) (HAProxyServerStats, error) {
	bytesIn, err := csvTrafficCounter(record, columns.bin, "bin")
	if err != nil {
		return HAProxyServerStats{}, err
	}
	bytesOut, err := csvTrafficCounter(record, columns.bout, "bout")
	if err != nil {
		return HAProxyServerStats{}, err
	}
	return HAProxyServerStats{
		Address:           metricText(csvField(record, columns.addr)),
		Status:            metricText(csvField(record, columns.status)),
		CheckStatus:       metricText(csvField(record, columns.checkStatus)),
		CheckCode:         csvUint(record, columns.checkCode),
		CheckDurationMS:   csvUint(record, columns.checkDuration),
		CheckDescription:  metricText(csvField(record, columns.checkDesc)),
		SessionsCurrent:   csvUint(record, columns.scur),
		SessionsTotal:     csvUint(record, columns.stot),
		SessionRate:       csvUint(record, columns.rate),
		QueueCurrent:      csvUint(record, columns.qcur),
		QueueMax:          csvUint(record, columns.qmax),
		BytesIn:           bytesIn,
		BytesOut:          bytesOut,
		LastChangeSeconds: csvUint(record, columns.lastchg),
		DowntimeSeconds:   csvUint(record, columns.downtime),
		Weight:            csvUint(record, columns.weight),
		Active:            csvUint(record, columns.act) != 0,
		Backup:            csvUint(record, columns.bck) != 0,
	}, nil
}

func csvField(record []string, index int) string {
	if index < 0 || index >= len(record) {
		return ""
	}
	return strings.TrimSpace(record[index])
}

func csvUint(record []string, index int) uint64 {
	raw := csvField(record, index)
	if raw == "" {
		return 0
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0
	}
	return value
}

func csvTrafficCounter(record []string, index int, name string) (uint64, error) {
	raw := csvField(record, index)
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid HAProxy show stat %s counter", name)
	}
	return value, nil
}

func metricName(value string) (string, bool) {
	value = strings.TrimSpace(value)
	return value, value != "" && len(value) <= maxHAProxyMetricNameBytes && !strings.ContainsRune(value, '\x00')
}

func metricText(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > maxHAProxyMetricTextBytes {
		value = value[:maxHAProxyMetricTextBytes]
	}
	return value
}

func saturatingAdd(left, right uint64) uint64 {
	if math.MaxUint64-left < right {
		return math.MaxUint64
	}
	return left + right
}

// ppTrustedCommandBytes bounds one runtime CLI request line. HAProxy reads a
// command line into one buffer (tune.bufsize, 16 KiB by default), so long
// "add acl" batches are split across connections.
const ppTrustedCommandBytes = 8 * 1024

func validACLReference(ref string) bool {
	if ref == "" || len(ref) > 255 || !filepath.IsAbs(ref) {
		return false
	}
	for _, char := range ref {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') ||
			char == '_' || char == '-' || char == '.' || char == '/') {
			return false
		}
	}
	return true
}

func validACLAddress(value string) bool {
	_, err := netip.ParseAddr(value)
	return err == nil && !strings.ContainsAny(value, "; \t\r\n%")
}

// ShowACL returns the patterns of the current version of the ACL whose
// reference is ref (the file path it was loaded from). present is false when
// the running HAProxy has no such ACL.
func (c HAProxySocketClient) ShowACL(ctx context.Context, ref string) ([]string, bool, error) {
	if err := validateHAProxySocketPath(c.Path); err != nil {
		return nil, false, err
	}
	if !validACLReference(ref) {
		return nil, false, errors.New("invalid HAProxy ACL reference")
	}
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	response, err := c.queryRaw(ctx, "show acl "+ref)
	if err != nil {
		return nil, false, err
	}
	text := string(response)
	if strings.Contains(text, "Unknown ACL identifier") {
		return nil, false, nil
	}
	var values []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// "<ptr> <pattern>"
		fields := strings.Fields(line)
		if len(fields) != 2 || !strings.HasPrefix(fields[0], "0x") {
			return nil, false, errors.New("unexpected show acl response: " + metricText(line))
		}
		values = append(values, fields[1])
	}
	return values, true, nil
}

// ReplaceACL atomically replaces every pattern of the ACL referenced by ref
// with values: prepare acl creates a new empty version, add acl @ver fills
// it, commit acl @ver makes it current and drops the older versions. Readers
// see either the old or the new set, never a partial one.
func (c HAProxySocketClient) ReplaceACL(ctx context.Context, ref string, values []string) error {
	if err := validateHAProxySocketPath(c.Path); err != nil {
		return err
	}
	if !validACLReference(ref) {
		return errors.New("invalid HAProxy ACL reference")
	}
	for _, value := range values {
		if !validACLAddress(value) {
			return errors.New("invalid HAProxy ACL address")
		}
	}
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	response, err := c.queryRaw(ctx, "prepare acl "+ref)
	if err != nil {
		return err
	}
	text := strings.TrimSpace(string(response))
	versionText, ok := strings.CutPrefix(text, "New version created:")
	version, convErr := strconv.ParseUint(strings.TrimSpace(versionText), 10, 32)
	if !ok || convErr != nil {
		return errors.New("HAProxy rejected prepare acl: " + metricText(text))
	}
	at := "@" + strconv.FormatUint(version, 10) + " " + ref
	var batch []string
	size := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		out, err := c.queryRaw(ctx, strings.Join(batch, ";"))
		batch, size = batch[:0], 0
		if err != nil {
			return err
		}
		if text := strings.TrimSpace(string(out)); text != "" {
			return errors.New("HAProxy rejected add acl: " + metricText(text))
		}
		return nil
	}
	for _, value := range values {
		command := "add acl " + at + " " + value
		if size+len(command)+1 > ppTrustedCommandBytes {
			if err := flush(); err != nil {
				return err
			}
		}
		batch = append(batch, command)
		size += len(command) + 1
	}
	if err := flush(); err != nil {
		return err
	}
	out, err := c.queryRaw(ctx, "commit acl "+at)
	if err != nil {
		return err
	}
	if text := strings.TrimSpace(string(out)); text != "" {
		return errors.New("HAProxy rejected commit acl: " + metricText(text))
	}
	return nil
}
