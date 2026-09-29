package panel

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lintCodes(result LintResult) []string {
	codes := make([]string, 0, len(result.Issues))
	for _, issue := range result.Issues {
		codes = append(codes, issue.Severity+":"+issue.Code)
	}
	return codes
}

func requireCleanLint(t *testing.T, name, cfg string) LintResult {
	t.Helper()
	result := LintHAProxyConfig(cfg)
	if len(result.Issues) > 0 {
		lines := strings.Split(cfg, "\n")
		for _, issue := range result.Issues {
			t.Errorf("%s: line %d col %d %s %s: %s | %q", name, issue.Line, issue.Column, issue.Severity, issue.Code, issue.Message, lines[issue.Line-1])
		}
	}
	require.True(t, result.Valid, name)
	return result
}

func TestLintGoldenRenderedConfigsAreClean(t *testing.T) {
	files, err := filepath.Glob("testdata/*.golden.cfg")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	for _, file := range files {
		raw, err := os.ReadFile(file)
		require.NoError(t, err)
		requireCleanLint(t, file, string(raw))
	}
	raw, err := os.ReadFile("testdata/haproxy_render.golden.cfg")
	require.NoError(t, err)
	result := LintHAProxyConfig(string(raw))
	assert.Equal(t, []int{443, 8443}, result.ListenerTCPPorts)
	require.NotEmpty(t, result.Sections)
	assert.Equal(t, LintSection{Type: "global", Name: "", Line: 4}, result.Sections[0])
	assert.Contains(t, result.Sections, LintSection{Type: "frontend", Name: "nf_fe_any_443_99ed27c0", Line: 49})
}

// Every route shape the editor can produce (the UI contract fixture) must
// render into a configuration with zero lint errors and zero warnings, and the
// linter's listener ports must equal the renderer's.
func TestLintRenderedUIRouteMatrixIsClean(t *testing.T) {
	file, err := os.Open("testdata/ui_route_payloads.json.gz")
	require.NoError(t, err)
	defer file.Close()
	reader, err := gzip.NewReader(file)
	require.NoError(t, err)
	var cases []uiRoutePayloadCase
	require.NoError(t, json.NewDecoder(reader).Decode(&cases))
	facts := NodeRenderFacts{}
	checked := 0
	for _, c := range cases {
		in := decodeRouteInput(t, string(c.Payload))
		spec, err := validateRoute(in, c.Method == "PUT")
		if err != nil {
			continue
		}
		route := specToRoute(spec)
		if strings.TrimSpace(route.CustomFragment) != "" {
			// Operator-written backend directives are linted as written.
			continue
		}
		for _, variant := range []NodeRenderFacts{facts, {HAProxyLogsDisabled: true}} {
			got, err := RenderHAProxyConfigForNode([]Route{route}, variant)
			if err != nil {
				continue
			}
			result := LintHAProxyConfigForNode(got.Config, got.RuntimeNames)
			if len(result.Issues) > 0 {
				requireCleanLint(t, c.Name, got.Config)
				t.Fatalf("%s: runtime-name issues %v", c.Name, lintCodes(result))
			}
			require.ElementsMatch(t, got.ListenerPorts, result.ListenerTCPPorts, c.Name)
			checked++
		}
	}
	require.Greater(t, checked, 4000)
}

func TestLintRenderedMultiRouteConfigIsClean(t *testing.T) {
	quota := int64(1 << 30)
	down, up := int64(50), int64(20)
	routes := []Route{
		{ID: "11111111-1111-4111-8111-111111111111", ListenerIP: "*", ListenerPort: 443, MatchMode: "sni",
			SNIs: []string{"a.example.com"}, Hostname: "a.example.com", TargetType: "tcp", TargetHost: "origin.example.com",
			TargetPort: 8443, ProxyProtocol: "v2", HealthCheck: true, Enabled: true, QuotaBytes: &quota, QuotaAction: "block_new",
			QuotaPeriod: "calendar_month", ClientDownloadMbps: &down, ClientUploadMbps: &up, AcceptProxyFrom: []string{"10.0.0.0/8", "edge.example.net"},
			CustomFragment: "  timeout server 1h\n"},
		{ID: "22222222-2222-4222-8222-222222222222", ListenerIP: "*", ListenerPort: 443, MatchMode: "any_tcp", Fallback: true,
			TargetType: "unix", UnixSocketPath: "/dev/shm/x.sock", ProxyProtocol: "v1", HealthCheck: true, Enabled: true},
		{ID: "33333333-3333-4333-8333-333333333333", ListenerIP: "2001:db8::1", ListenerPort: 8443, MatchMode: "sni",
			SNIs: []string{"b.example.com"}, Hostname: "b.example.com", TargetType: "tcp", TargetHost: "pool.example.com",
			TargetPort: 443, DNSPool: true, ProxyProtocol: "none", HealthCheck: true, Enabled: true,
			StickyMode: StickyModeSourceTable, BalanceAlgorithm: BalanceAlgorithmLeastConn},
		{ID: "44444444-4444-4444-8444-444444444444", ListenerIP: "192.0.2.5", ListenerPort: 10000, MatchMode: "any_tcp", Fallback: true,
			TargetType: "tcp", TargetHost: "192.0.2.9", TargetPort: 443, ProxyProtocol: "none", Enabled: true,
			Servers: []RouteServer{
				{Name: "a", TargetType: "tcp", Host: "192.0.2.10", Port: 443, Position: 0, Weight: 10},
				{Name: "b", TargetType: "tcp", Host: "pool.example.com", Port: 443, Position: 1, DNSPool: true, Backup: true},
			}, BalanceMode: "failover", StickyMode: StickyModeNone},
	}
	got, err := RenderHAProxyConfigForNode(routes, NodeRenderFacts{})
	require.NoError(t, err)
	result := requireCleanLint(t, "multi", got.Config)
	assert.ElementsMatch(t, got.ListenerPorts, result.ListenerTCPPorts)
	full := LintHAProxyConfigForNode(got.Config, got.RuntimeNames)
	assert.Empty(t, full.Issues)

	// Dropping a generated backend reports the missing runtime names.
	edited := strings.Replace(got.Config, "\nbackend nf_be_222222222222", "\nbackend renamed_be", 1)
	edited = strings.Replace(edited, "default_backend nf_be_222222222222", "default_backend renamed_be", 1)
	missing := LintHAProxyConfigForNode(edited, got.RuntimeNames)
	assert.True(t, missing.Valid)
	assert.Contains(t, lintCodes(missing), "warning:runtime_name_missing")
}

func TestLintDetectsErrors(t *testing.T) {
	cfg := strings.Join([]string{
		"maxconn 10",                   // 1 outside section
		"global",                       // 2
		"    daemon",                   // 3
		"defaults",                     // 4
		"    timeout client 10x",       // 5 invalid timeout
		"    timeout bogus 10s",        // 6 unknown kind
		"frontend",                     // 7 missing name
		"frontend fe",                  // 8
		"    bind :70000",              // 9 invalid port
		"    bind [::1]:443,:80-90",    // 10 ok
		"    bind unix@/run/x.sock",    // 11 ignored
		"    use_backend nope if TRUE", // 12 undefined
		"    use_backend %[var(txn.be)]",
		"    frobnicate on", // 14 unknown directive
		"backend be",        // 15
		"    server s1 10.0.0.1:0",
		"    server s1 10.0.0.2:80",
		"    server s2",
		"    server s3 /run/sock",
		"    server s4 [2001:db8::1]:443",
		"backend be", // 21 duplicate
		"bogus section",
		"    .if defined(X)",
		"    .else",
		"    .else",
		"",
	}, "\n")
	result := LintHAProxyConfig(cfg)
	assert.False(t, result.Valid)
	byLine := map[int][]string{}
	for _, issue := range result.Issues {
		byLine[issue.Line] = append(byLine[issue.Line], issue.Severity+":"+issue.Code)
	}
	assert.Equal(t, []string{"error:outside_section"}, byLine[1])
	assert.Equal(t, []string{"error:invalid_timeout"}, byLine[5])
	assert.Equal(t, []string{"error:unknown_timeout"}, byLine[6])
	assert.Equal(t, []string{"error:missing_name"}, byLine[7])
	assert.Equal(t, []string{"error:invalid_port"}, byLine[9])
	assert.Empty(t, byLine[10])
	assert.Empty(t, byLine[11])
	assert.Equal(t, []string{"error:undefined_backend"}, byLine[12])
	assert.Empty(t, byLine[13])
	assert.Equal(t, []string{"error:unknown_directive"}, byLine[14])
	assert.Equal(t, []string{"error:invalid_port"}, byLine[16])
	assert.Equal(t, []string{"error:duplicate_server"}, byLine[17])
	assert.Equal(t, []string{"error:server_missing_address"}, byLine[18])
	assert.Empty(t, byLine[19])
	assert.Empty(t, byLine[20])
	assert.Equal(t, []string{"error:duplicate_proxy"}, byLine[21])
	assert.Equal(t, []string{"error:unknown_section"}, byLine[22])
	assert.Equal(t, []string{"error:cond_unbalanced"}, byLine[23])
	assert.Equal(t, []string{"error:cond_unbalanced"}, byLine[25])
	ports := make([]int, 0)
	ports = append(ports, 443)
	for p := 80; p <= 90; p++ {
		ports = append(ports, p)
	}
	assert.ElementsMatch(t, ports, result.ListenerTCPPorts)
	issue := result.Issues[0]
	assert.Equal(t, LintIssue{Line: 1, Column: 1, EndColumn: 8, Severity: "error", Code: "outside_section", Message: issue.Message}, issue)
}

func TestLintRejectsBinaryAndOversized(t *testing.T) {
	result := LintHAProxyConfig("global\n    daemon\x00\n")
	assert.Contains(t, lintCodes(result), "error:nul_byte")
	result = LintHAProxyConfig("global\n    daemon \xff\n")
	assert.Contains(t, lintCodes(result), "error:invalid_utf8")
	result = LintHAProxyConfig(strings.Repeat("#", MaxManagedConfigBytes+1))
	assert.Equal(t, []string{"error:config_too_large"}, lintCodes(result))
	result = LintHAProxyConfig("global\n    .if defined(A)\n")
	assert.Contains(t, lintCodes(result), "error:cond_unbalanced")
	result = LintHAProxyConfig("global\n    log \"unterminated\n")
	assert.Contains(t, lintCodes(result), "error:unterminated_quote")
}

func TestLintDoesNotCountBackendOrUDPBindsAsListenerPorts(t *testing.T) {
	cfg := "global\n    daemon\npeers p\n    bind 127.0.0.1:10000\n    server local\nlog-forward lf\n    dgram-bind 127.0.0.1:514\n    bind 127.0.0.1:1514\n    log global\nlisten stats\n    bind 127.0.0.1:8404,quic4@:443\n    mode http\n"
	result := requireCleanLint(t, "misc", cfg)
	assert.Equal(t, []int{8404}, result.ListenerTCPPorts)
}

func TestLintKeywordsJSONInSync(t *testing.T) {
	frontend, err := os.ReadFile("../../frontend/src/features/haproxy-editor/haproxyKeywords.json")
	require.NoError(t, err)
	require.True(t, bytes.Equal(frontend, haproxyKeywordsJSON),
		"internal/panel/haproxy_keywords.json must be a byte-for-byte copy of frontend/src/features/haproxy-editor/haproxyKeywords.json")
}

type lintCase struct {
	name   string
	cfg    string
	line   int
	code   string
	column int // 0 = do not check
	end    int
}

// Mistakes HAProxy 3.x rejects with a fatal "parsing" alert. The same table
// is mirrored in frontend/tests/haproxyEditor.test.ts.
func TestLintStrictKeywordErrors(t *testing.T) {
	cases := []lintCase{
		{"option trailing comma", "defaults\n    option tcplog,\n", 2, "unknown_option", 18, 19},
		{"option typo", "defaults\n    option tcplogg\n", 2, "unknown_option", 12, 19},
		{"no option typo", "defaults\n    no option tcplogg\n", 2, "unknown_option", 15, 22},
		{"option args", "defaults\n    option dontlognull 1\n", 2, "too_many_args", 0, 0},
		{"option in global", "global\n    option tcplog\n", 2, "unknown_directive", 0, 0},
		{"server keyword typo", "backend b\n    server s1 1.2.3.4:443 chek\n", 2, "unknown_server_keyword", 27, 31},
		{"server keyword comma", "backend b\n    server s1 1.2.3.4:443 check,\n", 2, "stray_punctuation", 32, 33},
		{"server-template keyword", "backend b\n    server-template s 3 x.example.com:443 check resolverz dns\n", 2, "unknown_server_keyword", 0, 0},
		{"default-server keyword", "backend b\n    default-server inetr 3s\n", 2, "unknown_server_keyword", 0, 0},
		{"server bad inter", "backend b\n    server s1 1.2.3.4:443 check inter 5x\n", 2, "invalid_duration", 0, 0},
		{"server missing value", "backend b\n    server s1 1.2.3.4:443 check inter\n", 2, "missing_value", 0, 0},
		{"bind keyword typo", "frontend f\n    bind :443 acept-proxy\n", 2, "unknown_bind_keyword", 15, 26},
		{"bind in backend", "backend b\n    bind :443\n", 2, "keyword_not_allowed", 5, 9},
		{"bind in defaults", "defaults\n    bind :443\n", 2, "keyword_not_allowed", 0, 0},
		{"server in frontend", "frontend f\n    server s 1.2.3.4:80\n", 2, "keyword_not_allowed", 0, 0},
		{"use_backend in backend", "backend b\n    use_backend b\n", 2, "keyword_not_allowed", 0, 0},
		{"tcp-request connection in backend", "backend b\n    tcp-request connection accept\n", 2, "keyword_not_allowed", 0, 0},
		{"tcp-request in anonymous defaults", "defaults\n    tcp-request inspect-delay 5s\n", 2, "keyword_not_allowed", 0, 0},
		{"tcp-request sub typo", "frontend f\n    tcp-request contnet accept\n", 2, "unknown_keyword", 0, 0},
		{"timeout typo", "defaults\n    timeout clinet 5s\n", 2, "unknown_timeout", 13, 19},
		{"timeout comma", "defaults\n    timeout client, 5s\n", 2, "stray_punctuation", 19, 20},
		{"mode typo", "defaults\n    mode tpc\n", 2, "invalid_mode", 0, 0},
		{"mode comma", "defaults\n    mode tcp,\n", 2, "stray_punctuation", 13, 14},
		{"mode health", "defaults\n    mode health\n", 2, "invalid_mode", 0, 0},
		{"mode log in frontend", "frontend f\n    mode log\n", 2, "invalid_mode", 0, 0},
		{"balance typo", "backend b\n    balance roundrobbin\n", 2, "invalid_balance", 0, 0},
		{"hash-type typo", "backend b\n    hash-type consistant\n", 2, "invalid_hash_type", 0, 0},
		{"log facility typo", "global\n    log /dev/log locl0\n", 2, "invalid_log", 0, 0},
		{"log level typo", "global\n    log /dev/log local0 notise\n", 2, "invalid_log", 0, 0},
		{"directive typo in defaults", "defaults\n    maxconnn 10\n", 2, "unknown_directive", 0, 0},
		{"directive comma", "defaults\n    maxconn, 10\n", 2, "stray_punctuation", 12, 13},
		{"trailing semicolon", "defaults\n    retries 3;\n", 2, "stray_punctuation", 14, 15},
		{"global typo", "global\n    nbthreads 2\n", 2, "unknown_directive", 0, 0},
		{"negation unsupported", "defaults\n    no retries 3\n", 2, "negation_not_supported", 0, 0},
		{"no on tcplog", "defaults\n    no option tcplog\n", 2, "negation_not_supported", 0, 0},
		{"stats socket keyword", "global\n    stats socket /run/h.sock mode 660 levle admin\n", 2, "unknown_bind_keyword", 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			result := LintHAProxyConfig(c.cfg)
			assert.False(t, result.Valid, lintCodes(result))
			var found *LintIssue
			for i := range result.Issues {
				issue := result.Issues[i]
				if issue.Line == c.line && issue.Code == c.code {
					found = &issue
					break
				}
			}
			require.NotNil(t, found, "want %d:%s, got %v", c.line, c.code, result.Issues)
			assert.Equal(t, LintSeverityError, found.Severity)
			if c.column > 0 {
				assert.Equal(t, []int{c.column, c.end}, []int{found.Column, found.EndColumn}, found.Message)
			}
		})
	}
	issue := LintHAProxyConfig("defaults\n    option tcplog,\n").Issues[0]
	assert.Equal(t, "Неизвестная опция «tcplog,» — лишний символ «,»", issue.Message)
}

func TestLintAcceptsValidHAProxy3Syntax(t *testing.T) {
	cfg := strings.Join([]string{
		"global",
		"    log stdout format raw local0 info",
		"    log 127.0.0.1:514 len 2048 local1 notice err",
		"    no busy-polling",
		"    stats socket ipv4@127.0.0.1:9999 level admin expose-fd listeners",
		"    tune.ssl.default-dh-param 2048",
		"    ssl-default-bind-options ssl-min-ver TLSv1.2 no-tls-tickets",
		"    set-var proc.x str(a,b)",
		"    cpu-map auto:1/1-4 0-3",
		"defaults named",
		"    mode http",
		"    option httplog clf",
		"    no option http-server-close",
		"    default option redispatch",
		"    option forwardfor except 127.0.0.0/8 header X-Real-IP",
		"    option httpchk GET /health",
		"    option redispatch 1",
		"    tcp-request inspect-delay 5s",
		`    http-request set-header X-A "a, b;" if { path_beg /a, }`,
		`    log-format "%ci:%cp [%tr] %ft %b/%s %TR/%Tw/%Tc/%Tr/%Ta %ST %B %CC %CS %tsc %ac/%fc/%bc/%sc/%rc %sq/%bq %hr %hs %{+Q}r"`,
		"    timeout http-request 10s",
		"    timeout http-keep-alive 2s",
		"    balance random(2)",
		"    hash-type map-based crc32",
		"    no log",
		"frontend web from named",
		"    bind :443 ssl crt /etc/ssl/a.pem alpn h2,http/1.1 ssl-min-ver TLSv1.2 accept-proxy",
		"    bind quic4@:443 ssl crt /etc/ssl/a.pem alpn h3 thread 1-2 shards by-thread",
		"    bind unix@/run/x.sock mode 600 user haproxy group haproxy",
		"    capture request header Host len 64",
		"    rate-limit sessions 100",
		"    stats uri /stats",
		"    stick-table type ip size 1m expire 10m store http_req_rate(10s)",
		"    tcp-request connection track-sc0 src",
		"    use_backend app if { hdr(host) -i a.example.com }",
		"    default_backend app",
		"backend app",
		"    balance hdr(host)",
		"    hash-type consistent sdbm avalanche",
		"    http-check send meth GET uri /health ver HTTP/1.1 hdr Host a",
		"    http-check expect status 200",
		"    stick on src",
		"    default-server inter 3s fall 3 rise 2 check-sni a.example.com",
		"    server s1 10.0.0.1:443 ssl verify none sni str(a) check weight 100 maxconn 50 source 10.0.0.9 usesrc clientip",
		"    server s2 10.0.0.2:443 check agent-check agent-port 8080 agent-inter 5s on-marked-down shutdown-sessions",
		"    server s3 10.0.0.3:443 set-proxy-v2-tlv-fmt(0x20) %[fc_pp_tlv(0x20)] send-proxy-v2 proxy-v2-options ssl,cert-cn",
		"    server s4 10.0.0.4:80 track app/s1 cookie s4 observe layer4 error-limit 10 on-error mark-down",
		"    server-template srv 1-3 pool.example.com:443 resolvers dns resolve-prefer ipv4 init-addr none",
		"listen stats",
		"    bind 127.0.0.1:8404",
		"    mode http",
		"    stats enable",
		"    stats refresh 10s",
		"resolvers dns",
		"    nameserver ns1 1.1.1.1:53",
		"    accepted_payload_size 8192",
		"",
	}, "\n")
	requireCleanLint(t, "haproxy3", cfg)
}
