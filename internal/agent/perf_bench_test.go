package agent

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// syntheticShowStat builds a realistic "show stat" CSV with the full HAProxy
// 2.8 column set so parser benchmarks reflect production row widths.
func syntheticShowStat(servers int) []byte {
	columns := []string{"pxname", "svname", "qcur", "qmax", "scur", "smax", "slim", "stot", "bin", "bout",
		"dreq", "dresp", "ereq", "econ", "eresp", "wretr", "wredis", "status", "weight", "act", "bck",
		"chkfail", "chkdown", "lastchg", "downtime", "qlimit", "pid", "iid", "sid", "throttle", "lbtot",
		"tracked", "type", "rate", "rate_lim", "rate_max", "check_status", "check_code", "check_duration",
		"hrsp_1xx", "hrsp_2xx", "hrsp_3xx", "hrsp_4xx", "hrsp_5xx", "hrsp_other", "hanafail", "req_rate",
		"req_rate_max", "req_tot", "cli_abrt", "srv_abrt", "comp_in", "comp_out", "comp_byp", "comp_rsp",
		"lastsess", "last_chk", "last_agt", "qtime", "ctime", "rtime", "ttime", "agent_status", "agent_code",
		"agent_duration", "check_desc", "agent_desc", "check_rise", "check_fall", "check_health",
		"agent_rise", "agent_fall", "agent_health", "addr", "cookie", "mode", "algo", "conn_rate",
		"conn_rate_max", "conn_tot", "intercepted", "dcon", "dses", "wrew", "connect", "reuse",
		"cache_lookups", "cache_hits", "srv_icur", "src_ilim", "qtime_max", "ctime_max", "rtime_max",
		"ttime_max", "eint", "idle_conn_cur", "safe_conn_cur", "used_conn_cur", "need_conn_est",
		"uweight", "agg_server_status", "agg_server_check_status", "agg_check_status"}
	index := make(map[string]int, len(columns))
	for i, name := range columns {
		index[name] = i
	}
	var out bytes.Buffer
	out.WriteString("# " + strings.Join(columns, ",") + ",\n")
	row := func(values map[string]string) {
		fields := make([]string, len(columns))
		for name, value := range values {
			fields[index[name]] = value
		}
		out.WriteString(strings.Join(fields, ",") + ",\n")
	}
	backends := servers / 10
	if backends < 1 {
		backends = 1
	}
	row(map[string]string{"pxname": "edge", "svname": "FRONTEND", "scur": "7", "slim": "50000", "stot": "1200",
		"bin": "111", "bout": "222", "status": "OPEN", "type": "0", "rate": "5"})
	for b := 0; b < backends; b++ {
		backend := fmt.Sprintf("nf_be_%032d", b)
		for s := 0; s < servers/backends; s++ {
			row(map[string]string{"pxname": backend, "svname": fmt.Sprintf("nf_srv_%032d", s), "qcur": "1",
				"qmax": "3", "scur": "4", "stot": "900", "bin": "333", "bout": "444", "status": "UP",
				"weight": "10", "act": "1", "bck": "0", "lastchg": "17", "downtime": "2", "type": "2", "rate": "3",
				"check_status": "L7OK", "check_code": "200", "check_duration": "12",
				"check_desc": "Layer7 check passed", "addr": "192.0.2.10:443"})
		}
		row(map[string]string{"pxname": backend, "svname": "BACKEND", "qcur": "1", "stot": "1600", "bin": "555",
			"bout": "666", "status": "UP", "type": "1", "rate": "4"})
	}
	return out.Bytes()
}

func BenchmarkParseHAProxyShowStat1000(b *testing.B) {
	payload := syntheticShowStat(1000)
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := parseHAProxyShowStat(payload); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkHAProxySocketClientCollect1000(b *testing.B) {
	info, err := os.ReadFile("testdata/haproxy_show_info.txt")
	if err != nil {
		info = []byte("Pid: 1\nStart_time_sec: 2\nReloads: 0\nCurrConns: 1\nCumConns: 2\nConnRate: 0\n")
	}
	stat := syntheticShowStat(1000)
	socketPath := filepath.Join(b.TempDir(), "haproxy.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		b.Fatal(err)
	}
	defer listener.Close()
	go serveBenchHAProxy(listener, info, stat)
	client := HAProxySocketClient{Path: socketPath, Timeout: 2 * time.Second}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := client.Collect(context.Background()); err != nil {
			b.Fatal(err)
		}
	}
}

// serveBenchHAProxy answers every command line on a connection, emulating
// the HAProxy CLI which terminates each response with an empty line.
func serveBenchHAProxy(listener net.Listener, info, stat []byte) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			reader := bufio.NewReader(conn)
			for {
				line, readErr := reader.ReadString('\n')
				for _, command := range strings.Split(strings.TrimSpace(line), ";") {
					switch strings.TrimSpace(command) {
					case "show info":
						_, _ = conn.Write(info)
						_, _ = conn.Write([]byte("\n"))
					case "show stat":
						_, _ = conn.Write(stat)
						_, _ = conn.Write([]byte("\n"))
					}
				}
				if readErr != nil {
					return
				}
			}
		}()
	}
}

func BenchmarkObservedConfigState512KiB(b *testing.B) {
	dir := b.TempDir()
	path := filepath.Join(dir, "haproxy.cfg")
	if err := os.WriteFile(path, bytes.Repeat([]byte("# padding line for benchmark\n"), (512<<10)/29), 0o600); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(path+".revision", []byte("42\n"), 0o600); err != nil {
		b.Fatal(err)
	}
	manager := &ConfigManager{ManagedConfig: path}
	b.ReportAllocs()
	for b.Loop() {
		if manager.ObservedConfigState().SHA256 == "" {
			b.Fatal("missing checksum")
		}
	}
}

func BenchmarkHAProxyServiceSnapshot(b *testing.B) {
	runner := &serviceControlRunner{state: "active"}
	controller := &HAProxyServiceController{Runner: runner, Manager: &ConfigManager{}, ServiceName: "haproxy.service"}
	b.ReportAllocs()
	for b.Loop() {
		if controller.Snapshot(context.Background()).State != "active" {
			b.Fatal("unexpected state")
		}
	}
	b.ReportMetric(float64(len(runner.commands))/float64(b.N), "systemctl/op")
}

func BenchmarkParseCPUCounters64(b *testing.B) {
	var raw strings.Builder
	raw.WriteString("cpu  10132153 290696 3084719 46828483 16683 0 25195 0 0 0\n")
	for i := 0; i < 64; i++ {
		fmt.Fprintf(&raw, "cpu%d 1393280 32966 572056 13343292 6130 0 17875 0 0 0\n", i)
	}
	raw.WriteString("intr 199292226 22 0 0 0 0 0 0 0 1 0 0 0 0 0 0 0\nctxt 3423404\nbtime 1700000000\nprocesses 26342\n")
	payload := raw.String()
	b.ReportAllocs()
	for b.Loop() {
		if _, _, ok := parseCPUCounters(payload); !ok {
			b.Fatal("parse failed")
		}
	}
}

func BenchmarkNetworkRateSampler10(b *testing.B) {
	counters := make(map[string]uint64, 20)
	for i := 0; i < 10; i++ {
		counters[fmt.Sprintf("eth%d_rx", i)] = uint64(i * 1000)
		counters[fmt.Sprintf("eth%d_tx", i)] = uint64(i * 2000)
	}
	sampler := &NetworkRateSampler{}
	now := time.Unix(1700000000, 0)
	b.ReportAllocs()
	for b.Loop() {
		now = now.Add(15 * time.Second)
		for key := range counters {
			counters[key] += 100
		}
		sampler.Sample(counters, now)
	}
}

func BenchmarkCollectStats(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if _, err := CollectStats(); err != nil {
			b.Fatal(err)
		}
	}
}
