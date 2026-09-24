package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseHAProxyRuntimeFixtures(t *testing.T) {
	infoFixture, err := os.ReadFile("testdata/haproxy_show_info.txt")
	if err != nil {
		t.Fatal(err)
	}
	statFixture, err := os.ReadFile("testdata/haproxy_show_stat.csv")
	if err != nil {
		t.Fatal(err)
	}
	info, err := parseHAProxyShowInfo(infoFixture)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := parseHAProxyShowStat(statFixture)
	if err != nil {
		t.Fatal(err)
	}
	stats.ConnectionsCurrent = info.connectionsCurrent
	stats.ConnectionsTotal = info.connectionsTotal
	stats.ConnectionRate = info.connectionRate
	stats.CounterGeneration = info.counterGeneration()

	if stats.ConnectionsCurrent != 13 || stats.ConnectionsTotal != 4567 || stats.ConnectionRate != 9 {
		t.Fatalf("connections=%+v", stats)
	}
	if stats.CounterGeneration != "4312:1783930000:7" {
		t.Fatalf("counter generation=%q", stats.CounterGeneration)
	}
	if stats.BytesIn != 111 || stats.BytesOut != 222 {
		t.Fatalf("bytes_in=%d bytes_out=%d", stats.BytesIn, stats.BytesOut)
	}
	frontend := stats.Frontends["edge_tls"]
	if frontend.Status != "OPEN" || frontend.SessionsCurrent != 7 || frontend.SessionLimit != 50000 {
		t.Fatalf("frontend=%+v", frontend)
	}
	backend := stats.Backends["origin_pool"]
	if backend.Status != "UP" || backend.QueueCurrent != 1 || backend.SessionsTotal != 1600 {
		t.Fatalf("backend=%+v", backend)
	}
	server := stats.Servers["origin_pool"]["origin-a"]
	if server.Address != "192.0.2.10:443" || server.Status != "UP" || server.CheckStatus != "L7OK" || server.CheckCode != 200 || server.CheckDurationMS != 12 {
		t.Fatalf("server=%+v", server)
	}
	if !server.Active || server.Backup || server.LastChangeSeconds != 17 || server.DowntimeSeconds != 2 {
		t.Fatalf("server flags=%+v", server)
	}
	down := stats.Servers["origin_pool"]["origin-b"]
	if down.Status != "DOWN" || down.CheckStatus != "L4CON" || !down.Backup {
		t.Fatalf("down server=%+v", down)
	}
}

func TestHAProxySocketClientCollect(t *testing.T) {
	infoFixture, err := os.ReadFile("testdata/haproxy_show_info.txt")
	if err != nil {
		t.Fatal(err)
	}
	statFixture, err := os.ReadFile("testdata/haproxy_show_stat.csv")
	if err != nil {
		t.Fatal(err)
	}
	socketPath, done := serveHAProxyCLI(t, map[string][]byte{
		"show info": infoFixture,
		"show stat": statFixture,
	}, 1)
	client := HAProxySocketClient{Path: socketPath, Timeout: time.Second}
	stats, err := client.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.CounterGeneration != "4312:1783930000:7" || stats.ConnectionsTotal != 4567 || len(stats.Frontends) != 1 || len(stats.Servers["origin_pool"]) != 2 {
		t.Fatalf("stats=%+v", stats)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestHAProxySocketClientUnavailable(t *testing.T) {
	client := HAProxySocketClient{Path: filepath.Join(t.TempDir(), "missing.sock"), Timeout: 50 * time.Millisecond}
	stats, err := client.Collect(context.Background())
	if err == nil {
		t.Fatal("expected unavailable socket error")
	}
	if stats.Frontends == nil || stats.Backends == nil || stats.Servers == nil {
		t.Fatalf("maps must remain typed and non-nil: %+v", stats)
	}
}

func TestHAProxySocketClientRejectsPartialInfoWithoutStat(t *testing.T) {
	infoFixture, err := os.ReadFile("testdata/haproxy_show_info.txt")
	if err != nil {
		t.Fatal(err)
	}
	socketPath, done := serveHAProxyCLI(t, map[string][]byte{
		"show info": infoFixture,
		"show stat": []byte("not,a,stats,header\n"),
	}, 3)
	stats, err := (HAProxySocketClient{Path: socketPath, Timeout: time.Second}).Collect(context.Background())
	if err == nil {
		t.Fatal("partial info-only collection must not become a zero traffic snapshot")
	}
	if stats.BytesIn != 0 || stats.BytesOut != 0 || len(stats.Backends) != 0 {
		t.Fatalf("partial stats leaked into heartbeat: %+v", stats)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestHAProxySocketClientBoundsResponse(t *testing.T) {
	socketPath, done := serveHAProxyCLI(t, map[string][]byte{
		"show stat": []byte(strings.Repeat("x", 64)),
	}, 1)
	client := HAProxySocketClient{Path: socketPath, Timeout: time.Second, MaxResponseBytes: 16}
	_, err := client.query(context.Background(), "show stat")
	if err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("err=%v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestHAProxySocketClientSetsServerMaintenance(t *testing.T) {
	socketPath, done := serveHAProxyCLI(t, map[string][]byte{
		"disable server nf_be_11111111111141118111111111111111/nf_srv_11111111111141118111111111111111": nil,
		"enable server nf_be_11111111111141118111111111111111/nf_srv_11111111111141118111111111111111":  nil,
	}, 2)
	client := HAProxySocketClient{Path: socketPath, Timeout: time.Second}
	if err := client.SetServerMaintenance(context.Background(), "nf_be_11111111111141118111111111111111", "nf_srv_11111111111141118111111111111111", true); err != nil {
		t.Fatal(err)
	}
	if err := client.SetServerMaintenance(context.Background(), "nf_be_11111111111141118111111111111111", "nf_srv_11111111111141118111111111111111", false); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestHAProxySocketClientRejectsUnsafeMaintenanceNames(t *testing.T) {
	client := HAProxySocketClient{Path: filepath.Join(t.TempDir(), "missing.sock")}
	for _, object := range []string{"", "backend/server", "backend\ndisable server victim/server", "backend name"} {
		if err := client.SetServerMaintenance(context.Background(), object, "server", true); err == nil || !strings.Contains(err.Error(), "invalid HAProxy runtime object name") {
			t.Fatalf("object=%q err=%v", object, err)
		}
	}
}

func TestHAProxySocketClientRejectsMaintenanceCLIResponse(t *testing.T) {
	socketPath, done := serveHAProxyCLI(t, map[string][]byte{
		"disable server backend/server": []byte("No such backend.\n"),
	}, 1)
	err := (HAProxySocketClient{Path: socketPath, Timeout: time.Second}).SetServerMaintenance(context.Background(), "backend", "server", true)
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("err=%v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestHAProxySocketClientUsesTotalTimeoutBudget(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "haproxy.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan struct{})
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		close(accepted)
		defer conn.Close()
		_, _ = bufio.NewReader(conn).ReadString('\n')
		time.Sleep(150 * time.Millisecond)
	}()

	start := time.Now()
	_, err = (HAProxySocketClient{Path: socketPath, Timeout: 25 * time.Millisecond}).Collect(context.Background())
	if err == nil {
		t.Fatal("expected timeout")
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("collection exceeded total timeout budget: %s", elapsed)
	}
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("fake HAProxy did not accept connection")
	}
}

func TestParseHAProxyShowStatRejectsMissingHeader(t *testing.T) {
	_, err := parseHAProxyShowStat([]byte("not,a,stats,header\n"))
	if err == nil {
		t.Fatal("expected invalid header error")
	}
}

func TestParseHAProxyShowStatRejectsMalformedTrafficCountersAndRecovers(t *testing.T) {
	fixture, err := os.ReadFile("testdata/haproxy_show_stat.csv")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		oldCounter string
		badCounter string
		errorField string
	}{
		{name: "malformed bin", oldCounter: ",111,222,OPEN", badCounter: ",bad,222,OPEN", errorField: "bin"},
		{name: "overflow bout", oldCounter: ",888,1110,UP", badCounter: ",888,18446744073709551616,UP", errorField: "bout"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalid := strings.Replace(string(fixture), test.oldCounter, test.badCounter, 1)
			if invalid == string(fixture) {
				t.Fatal("fixture counter was not replaced")
			}
			stats, parseErr := parseHAProxyShowStat([]byte(invalid))
			if parseErr == nil || !strings.Contains(parseErr.Error(), test.errorField) {
				t.Fatalf("malformed counter must reject the whole snapshot: stats=%+v err=%v", stats, parseErr)
			}

			recovered, recoveryErr := parseHAProxyShowStat(fixture)
			if recoveryErr != nil {
				t.Fatalf("healthy sample after malformed input must recover: %v", recoveryErr)
			}
			if recovered.BytesIn != 111 || recovered.BytesOut != 222 {
				t.Fatalf("recovered counters=%d/%d", recovered.BytesIn, recovered.BytesOut)
			}
		})
	}
}

func serveHAProxyCLI(t *testing.T, responses map[string][]byte, connections int) (string, <-chan error) {
	t.Helper()
	socketPath := filepath.Join(t.TempDir(), "haproxy.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		defer listener.Close()
		for i := 0; i < connections; i++ {
			conn, err := listener.Accept()
			if err != nil {
				done <- err
				return
			}
			command, readErr := bufio.NewReader(conn).ReadString('\n')
			if readErr != nil {
				conn.Close()
				done <- readErr
				return
			}
			var response []byte
			ok := true
			// Like the HAProxy CLI, ';'-separated commands run in order and
			// each response is followed by an empty line.
			commands := strings.Split(strings.TrimSpace(command), ";")
			if exact, found := responses[strings.TrimSpace(command)]; found {
				commands = nil
				response = exact
			}
			for _, part := range commands {
				single, found := responses[strings.TrimSpace(part)]
				if !found {
					ok = false
					break
				}
				response = append(response, single...)
				if len(commands) > 1 {
					response = append(response, '\n')
				}
			}
			if !ok {
				conn.Close()
				done <- errors.New("unexpected command")
				return
			}
			_, writeErr := conn.Write(response)
			conn.Close()
			if writeErr != nil {
				done <- writeErr
				return
			}
		}
		done <- nil
	}()
	return socketPath, done
}

func csvReferenceRecords(t testing.TB, response []byte) ([][]string, bool) {
	reader := csv.NewReader(bytes.NewReader(response))
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	var records [][]string
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			return records, true
		}
		if err != nil {
			return nil, false
		}
		records = append(records, record)
	}
}

func checkShowStatRecordsMatchCSV(t testing.TB, response []byte) {
	want, wantOK := csvReferenceRecords(t, response)
	var got [][]string
	err := showStatRecords(response, func(record []string) error {
		got = append(got, append([]string(nil), record...))
		return nil
	})
	if (err == nil) != wantOK {
		t.Fatalf("input %q: err=%v csvOK=%t", response, err, wantOK)
	}
	if wantOK && !reflect.DeepEqual(got, want) {
		t.Fatalf("input %q:\n got=%q\nwant=%q", response, got, want)
	}
}

var showStatRecordSeeds = []string{
	"", "\n", "\r\n", "\r", "a", "a,b", "a,b\n", "a,b\r\n", "a,b\r", "a,b\r\r", "a,b\r\r\n",
	"a\rb,c\n", "\n\n# pxname,svname\nx,y\n\n", ",,,\n", "a,,b,\n", " a , b \n",
	"# pxname,svname,\"quoted\"\nx,y\n", "x,\"y\nz\",w\n", "x,y\"z\n", "\x00,\xff\n",
}

func TestShowStatRecordsMatchEncodingCSV(t *testing.T) {
	for _, seed := range showStatRecordSeeds {
		checkShowStatRecordsMatchCSV(t, []byte(seed))
	}
	fixture, err := os.ReadFile("testdata/haproxy_show_stat.csv")
	if err != nil {
		t.Fatal(err)
	}
	checkShowStatRecordsMatchCSV(t, fixture)
	checkShowStatRecordsMatchCSV(t, bytes.ReplaceAll(fixture, []byte("\n"), []byte("\r\n")))
	checkShowStatRecordsMatchCSV(t, syntheticShowStat(50))
}

func TestParseHAProxyShowStatMatchesQuotedFallback(t *testing.T) {
	plain := syntheticShowStat(200)
	fast, err := parseHAProxyShowStat(plain)
	if err != nil {
		t.Fatal(err)
	}
	// A quoted empty trailing header column forces the encoding/csv path
	// without changing any consumed value.
	quoted := bytes.Replace(plain, []byte(",\n"), []byte(",\"\"\n"), 1)
	slow, err := parseHAProxyShowStat(quoted)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fast, slow) {
		t.Fatal("fast path and encoding/csv path disagree")
	}
}

func FuzzShowStatRecordsMatchEncodingCSV(f *testing.F) {
	for _, seed := range showStatRecordSeeds {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, response []byte) {
		checkShowStatRecordsMatchCSV(t, response)
	})
}

func TestHAProxySocketClientFallsBackWhenCompoundCommandIsRejected(t *testing.T) {
	infoFixture, err := os.ReadFile("testdata/haproxy_show_info.txt")
	if err != nil {
		t.Fatal(err)
	}
	statFixture, err := os.ReadFile("testdata/haproxy_show_stat.csv")
	if err != nil {
		t.Fatal(err)
	}
	// The compound command answers with a CLI error instead of stats; the
	// client must retry with one connection per command.
	socketPath, done := serveHAProxyCLI(t, map[string][]byte{
		"show info;show stat": []byte("Unknown command.\n"),
		"show info":           infoFixture,
		"show stat":           statFixture,
	}, 3)
	stats, err := (HAProxySocketClient{Path: socketPath, Timeout: time.Second}).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.CounterGeneration != "4312:1783930000:7" || stats.BytesIn != 111 {
		t.Fatalf("stats=%+v", stats)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestHAProxySocketClientCompoundResponseRespectsPerCommandLimit(t *testing.T) {
	infoFixture, err := os.ReadFile("testdata/haproxy_show_info.txt")
	if err != nil {
		t.Fatal(err)
	}
	statFixture, err := os.ReadFile("testdata/haproxy_show_stat.csv")
	if err != nil {
		t.Fatal(err)
	}
	socketPath, done := serveHAProxyCLI(t, map[string][]byte{
		"show info": infoFixture,
		"show stat": statFixture,
	}, 1)
	client := HAProxySocketClient{Path: socketPath, Timeout: time.Second, MaxResponseBytes: int64(len(statFixture) - 1)}
	if _, err := client.Collect(context.Background()); err == nil {
		t.Fatal("oversized show stat section must be rejected")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSplitInfoAndStat(t *testing.T) {
	info, stat, ok := splitInfoAndStat([]byte("Pid: 1\nCurrConns: 2\n\n# pxname,svname\nfe,FRONTEND\n\n"))
	if !ok || string(info) != "Pid: 1\nCurrConns: 2\n\n" || string(stat) != "# pxname,svname\nfe,FRONTEND\n\n" {
		t.Fatalf("info=%q stat=%q ok=%t", info, stat, ok)
	}
	if info, stat, ok := splitInfoAndStat([]byte("# pxname,svname\n")); !ok || len(info) != 0 || string(stat) != "# pxname,svname\n" {
		t.Fatalf("stat-only info=%q stat=%q ok=%t", info, stat, ok)
	}
	if _, _, ok := splitInfoAndStat([]byte("Pid: 1\nUnknown command.\n")); ok {
		t.Fatal("missing stat header must not split")
	}
}
