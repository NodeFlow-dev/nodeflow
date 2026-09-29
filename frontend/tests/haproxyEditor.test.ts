// Run: npm test. Covers the haproxy.cfg editor helpers: docs coverage of every
// directive/option emitted by internal/panel/haproxy_renderer.go, hover
// resolution, formatter and the offline linter.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readdirSync, readFileSync } from 'node:fs';
import { argDocs, directiveDocs, keywordDocs, sectionDocs } from '../src/features/haproxy-editor/haproxyDocs.ts';
import KEYWORDS from '../src/features/haproxy-editor/haproxyKeywords.json' with { type: 'json' };
import { formatConfig, hoverMarkdown, lintConfig, resolveHover, tokenizeLine } from '../src/features/haproxy-editor/haproxyModel.ts';

const rendererSource = readFileSync(new URL('../../internal/panel/haproxy_renderer.go', import.meta.url), 'utf8');

/** String literals written into the config by the Go renderer. */
function renderedLiterals(): string[] {
  const literals: string[] = [];
  for (const line of rendererSource.split('\n')) {
    if (!/WriteString|Fprintf|condition :?=/.test(line)) continue;
    for (const match of line.matchAll(/"((?:[^"\\]|\\.)*)"/g)) {
      literals.push(match[1].replace(/\\n/g, '\n').replace(/\\t/g, '\t').replace(/%d/g, '1000').replace(/%s/g, '5s').replace(/%\.2f/g, '1.00'));
    }
  }
  return literals;
}

// Free-form names/values the renderer writes; they are not directives.
const VALUE_WORDS = new Set(['haproxy', 'splice', 'download', 'upload', 'renderer:', 'generated', 'by', 'nodeflow.', 'do', 'not', 'edit.', 'adguard_local', 'systemd_resolved', 'cloudflare', 'google', 'quad9']);

const allArgs = new Set(Object.values(argDocs).flatMap((group) => Object.keys(group)));
const hasDoc = (word: string) => Boolean(directiveDocs[word] || sectionDocs[word] || keywordDocs[word] || allArgs.has(word) || Object.keys(directiveDocs).some((key) => key.split(' ').includes(word)));

test('docs have an entry for every directive and option emitted by haproxy_renderer.go', () => {
  const missing = new Set<string>();
  let checked = 0;
  for (const literal of renderedLiterals()) {
    for (const rawLine of literal.split('\n')) {
      const line = rawLine.replace(/#.*/, '');
      for (const token of tokenizeLine(line)) {
        for (const part of token.text.toLowerCase().split(/[,()]/)) {
          if (!/^[.a-z][a-z0-9._-]*$/.test(part) || /^nf_/.test(part) || VALUE_WORDS.has(part)) continue;
          checked++;
          if (!hasDoc(part)) missing.add(part);
        }
      }
    }
  }
  assert.ok(checked > 80, `too few renderer tokens found (${checked}); did the renderer move?`);
  assert.deepEqual([...missing].sort(), []);
});

const RENDERED_SAMPLE = `global
    maxconn 20000
    nbthread 2
    log /dev/log local0
    stats socket /run/haproxy/admin.sock mode 660 level admin expose-fd listeners
    stats timeout 30s
    user haproxy
    tune.pipesize 262144
    localpeer nf_local
defaults
    log global
    mode tcp
    option tcplog
    clitcpka-idle 300s
    .if enabled(SPLICE)
        option splice-request
    .endif
    timeout connect 5s
    timeout client 15m
resolvers nf_dns
    nameserver cloudflare 1.1.1.1:53
    hold valid 10s
frontend nf_fe_any_443_abcd
    bind :443
    tcp-request connection expect-proxy layer4 if { src 10.0.0.0/8 } || nf_pp_trusted
    acl nf_pp_trusted src -f /etc/haproxy/pp.lst
    filter bwlim-out nf_bw_download_x limit 1000 key src table nf_bw_download_table_x min-size 2896
    tcp-request inspect-delay 5s
    tcp-request content accept if { req.ssl_hello_type 1 }
    acl nf_sni_x req.ssl_sni -i example.com
    use_backend nf_be_x if nf_sni_x
    default_backend nf_be_x
backend nf_be_x
    balance hash src,ipmask(32,64)
    hash-type consistent sdbm avalanche
    stick-table type ipv6 size 1m expire 1h peers nf_peers store bytes_out_rate(1s)
    stick on src
    option redispatch
    server nf_srv_x 192.0.2.10:443 weight 10 slowstart 30s check inter 5s fall 3 rise 2 send-proxy-v2 check-send-proxy backup
    server-template nf_srv_y_ 10 192.0.2.11:443 check resolvers nf_dns init-addr last,none resolve-opts prevent-dup-ip hash-key addr
`;

test('every keyword token of a rendered config resolves to a hover', () => {
  const lines = RENDERED_SAMPLE.split('\n');
  const free = new Set(['haproxy', 'cloudflare', 'example.com', 'splice']);
  lines.forEach((line, index) => {
    for (const token of tokenizeLine(line)) {
      for (const match of token.text.matchAll(/[^,()]+/g)) {
        const word = match[0];
        if (free.has(word.toLowerCase())) continue;
        const hover = resolveHover(lines, index, token.start + (match.index ?? 0));
        assert.ok(hover, `no hover for «${word}» in line ${index + 1}: ${line.trim()}`);
        assert.notEqual(hover.kind, 'fallback', `fallback hover for «${word}» in line ${index + 1}`);
      }
    }
  });
});

test('hover picks argument docs, generated names, case-insensitive', () => {
  const lines = RENDERED_SAMPLE.split('\n');
  const at = (lineText: string, word: string) => {
    const index = lines.findIndex((l) => l.includes(lineText));
    return resolveHover(lines, index, lines[index].indexOf(word) + 1);
  };
  assert.equal(at('check-send-proxy backup', 'check-send-proxy')?.doc, argDocs.server['check-send-proxy']);
  assert.equal(at('check-send-proxy backup', 'backup')?.doc, argDocs.server.backup);
  assert.match(at('backend nf_be_x', 'nf_be_x')?.doc.summary ?? '', /Не переименовывать/);
  assert.equal(at('timeout client 15m', 'client')?.title, 'timeout client');
  assert.match(at('timeout client 15m', '15m')?.value ?? '', /15 минут/);
  assert.equal(at('balance hash', 'hash')?.doc, argDocs.balance.hash);
  assert.equal(at('req.ssl_sni', 'req.ssl_sni')?.doc, keywordDocs['req.ssl_sni']);
  assert.equal(at('use_backend', ' if ')?.doc, keywordDocs.if);
  const upper = ['BACKEND x', '    SERVER a 1.2.3.4:80 CHECK'];
  assert.equal(resolveHover(upper, 0, 1)?.doc, sectionDocs.backend);
  assert.equal(resolveHover(upper, 1, upper[1].indexOf('CHECK'))?.doc, argDocs.server.check);
  const md = hoverMarkdown(at('check-send-proxy backup', 'check-send-proxy')!);
  assert.match(md, /```haproxy/);
  assert.match(md, /⚠ Если .*\*\*/);
});

test('formatter normalises indentation, blank lines and trailing spaces', () => {
  const input = '  global  \n maxconn 10   \n\n\n\n# note\ndefaults\n  mode tcp\n.if enabled(SPLICE)\noption splice-request\n.endif\nbackend b\n\tserver s 1.2.3.4:80\n';
  assert.equal(formatConfig(input), 'global\n    maxconn 10\n\n    # note\ndefaults\n    mode tcp\n    .if enabled(SPLICE)\n        option splice-request\n    .endif\nbackend b\n    server s 1.2.3.4:80\n');
  assert.equal(formatConfig(formatConfig(input)), formatConfig(input));
});

test('fallback linter accepts a rendered config', () => {
  const result = lintConfig(RENDERED_SAMPLE);
  assert.deepEqual(result.issues.filter((i) => i.severity === 'error'), []);
  assert.equal(result.valid, true);
  assert.deepEqual(result.listener_tcp_ports, [443]);
  assert.deepEqual(result.sections.map((s) => s.type), ['global', 'defaults', 'resolvers', 'frontend', 'backend']);
});

test('fallback linter reports typical mistakes', () => {
  const codes = (text: string) => lintConfig(text).issues.map((i) => `${i.line}:${i.code}`);
  assert.deepEqual(codes('defaults\n    timeout client 15x\n'), ['2:timeout_format']);
  assert.deepEqual(codes('frontend f\n    bind :70000\n'), ['2:port_range']);
  assert.deepEqual(codes('defaults\n    .if enabled(SPLICE)\n    mode tcp\n'), ['2:conditional_unbalanced']);
  assert.deepEqual(codes('defaults\n    .endif\n'), ['2:conditional_unbalanced']);
  assert.deepEqual(codes('frontnd f\n    bind :80\n'), ['1:outside_section', '2:outside_section']);
  assert.deepEqual(codes('frontend f\n    bind :80\n    use_backend nope\n'), ['3:unknown_backend']);
  assert.deepEqual(codes('frontend f\n    bind :80\n    mode tcp\n    frobnicate 1\n'), ['4:unknown_directive']);
  assert.equal(lintConfig('frontend f\n    bind :80\n    frobnicate 1\n').issues[0].severity, 'error');
  assert.equal(lintConfig('resolvers r\n    nameserver a 1.1.1.1:53\n    frobnicate 1\n').issues[0].severity, 'warning');
  const issue = lintConfig('defaults\n    timeout client 15x\n').issues[0];
  assert.deepEqual([issue.column, issue.end_column, issue.severity], [20, 23, 'error']);
  assert.equal(lintConfig('').valid, false);
});

test('keyword lists are shared with the Go linter byte for byte', () => {
  const ts = readFileSync(new URL('../src/features/haproxy-editor/haproxyKeywords.json', import.meta.url));
  const go = readFileSync(new URL('../../internal/panel/haproxy_keywords.json', import.meta.url));
  assert.ok(ts.equals(go), 'internal/panel/haproxy_keywords.json must equal frontend/src/features/haproxy-editor/haproxyKeywords.json');
});

test('fallback linter: every golden renderer config is clean (no errors, no warnings)', () => {
  const dir = new URL('../../internal/panel/testdata/', import.meta.url);
  const files = readdirSync(dir).filter((f) => f.endsWith('.golden.cfg'));
  assert.ok(files.length >= 4);
  for (const file of files) {
    const text = readFileSync(new URL(file, dir), 'utf8');
    const lines = text.split('\n');
    assert.deepEqual(lintConfig(text).issues.map((i) => `${i.line}:${i.code} ${lines[i.line - 1]}`), [], file);
  }
});

test('fallback linter: every directive line emitted by haproxy_renderer.go is clean', () => {
  // Wrap each rendered line into a section that accepts it and lint it alone.
  const wrappers = ['global\n', 'defaults\n', 'frontend f\n    bind :1\n', 'backend b\n', 'resolvers r\n', 'peers p\n'];
  let checked = 0;
  let fragments = 0;
  for (const literal of renderedLiterals()) {
    for (const raw of literal.split('\n')) {
      // " check inter 5s …", " send-proxy-v2", " backup": server option fragments.
      if (/^ [a-z][a-z0-9-]*( |$)/.test(raw) && !/^ {4}/.test(raw) && !raw.includes('=')) {
        const words = tokenizeLine(raw).map((t) => t.text);
        // Filter/stick-table tails (" min-size 2896") are covered by the goldens.
        if (!words.length || !(words[0] in KEYWORDS.server)) continue;
        const line = `backend b\n    server s 192.0.2.1:443${raw.trimEnd()}${raw.endsWith(' ') ? ' 1' : ''}\n`;
        assert.deepEqual(lintConfig(line).issues.map((i) => i.code), [], `server fragment: ${raw}`);
        fragments++;
        continue;
      }
      // Lines continued by string concatenation are checked through goldens.
      if (!/^ {4}[a-z]/.test(raw) || raw !== raw.trimEnd()) continue;
      const clean = wrappers.some((w) => lintConfig(w + raw + '\n').issues.length === 0);
      assert.ok(clean, `renderer line is not clean in any section: ${raw}`);
      checked++;
    }
  }
  assert.ok(fragments >= 8, `too few server option fragments (${fragments})`);
  assert.ok(checked > 50, `too few renderer lines (${checked})`);
});

// Mirrors TestLintStrictKeywordErrors in internal/panel/haproxy_lint_test.go.
const STRICT_CASES: [name: string, cfg: string, line: number, code: string, column?: number, end?: number][] = [
  ['option trailing comma', 'defaults\n    option tcplog,\n', 2, 'unknown_option', 18, 19],
  ['option typo', 'defaults\n    option tcplogg\n', 2, 'unknown_option', 12, 19],
  ['no option typo', 'defaults\n    no option tcplogg\n', 2, 'unknown_option', 15, 22],
  ['option args', 'defaults\n    option dontlognull 1\n', 2, 'too_many_args'],
  ['option in global', 'global\n    option tcplog\n', 2, 'unknown_directive'],
  ['server keyword typo', 'backend b\n    server s1 1.2.3.4:443 chek\n', 2, 'unknown_server_keyword', 27, 31],
  ['server keyword comma', 'backend b\n    server s1 1.2.3.4:443 check,\n', 2, 'stray_punctuation', 32, 33],
  ['server-template keyword', 'backend b\n    server-template s 3 x.example.com:443 check resolverz dns\n', 2, 'unknown_server_keyword'],
  ['default-server keyword', 'backend b\n    default-server inetr 3s\n', 2, 'unknown_server_keyword'],
  ['server bad inter', 'backend b\n    server s1 1.2.3.4:443 check inter 5x\n', 2, 'invalid_duration'],
  ['server missing value', 'backend b\n    server s1 1.2.3.4:443 check inter\n', 2, 'missing_value'],
  ['bind keyword typo', 'frontend f\n    bind :443 acept-proxy\n', 2, 'unknown_bind_keyword', 15, 26],
  ['bind in backend', 'backend b\n    bind :443\n', 2, 'keyword_not_allowed', 5, 9],
  ['bind in defaults', 'defaults\n    bind :443\n', 2, 'keyword_not_allowed'],
  ['server in frontend', 'frontend f\n    server s 1.2.3.4:80\n', 2, 'keyword_not_allowed'],
  ['use_backend in backend', 'backend b\n    use_backend b\n', 2, 'keyword_not_allowed'],
  ['tcp-request connection in backend', 'backend b\n    tcp-request connection accept\n', 2, 'keyword_not_allowed'],
  ['tcp-request in anonymous defaults', 'defaults\n    tcp-request inspect-delay 5s\n', 2, 'keyword_not_allowed'],
  ['tcp-request sub typo', 'frontend f\n    tcp-request contnet accept\n', 2, 'unknown_keyword'],
  ['timeout typo', 'defaults\n    timeout clinet 5s\n', 2, 'unknown_timeout', 13, 19],
  ['timeout comma', 'defaults\n    timeout client, 5s\n', 2, 'stray_punctuation', 19, 20],
  ['mode typo', 'defaults\n    mode tpc\n', 2, 'invalid_mode'],
  ['mode comma', 'defaults\n    mode tcp,\n', 2, 'stray_punctuation', 13, 14],
  ['mode health', 'defaults\n    mode health\n', 2, 'invalid_mode'],
  ['mode log in frontend', 'frontend f\n    mode log\n', 2, 'invalid_mode'],
  ['balance typo', 'backend b\n    balance roundrobbin\n', 2, 'invalid_balance'],
  ['hash-type typo', 'backend b\n    hash-type consistant\n', 2, 'invalid_hash_type'],
  ['log facility typo', 'global\n    log /dev/log locl0\n', 2, 'invalid_log'],
  ['log level typo', 'global\n    log /dev/log local0 notise\n', 2, 'invalid_log'],
  ['directive typo in defaults', 'defaults\n    maxconnn 10\n', 2, 'unknown_directive'],
  ['directive comma', 'defaults\n    maxconn, 10\n', 2, 'stray_punctuation', 12, 13],
  ['trailing semicolon', 'defaults\n    retries 3;\n', 2, 'stray_punctuation', 14, 15],
  ['global typo', 'global\n    nbthreads 2\n', 2, 'unknown_directive'],
  ['negation unsupported', 'defaults\n    no retries 3\n', 2, 'negation_not_supported'],
  ['no on tcplog', 'defaults\n    no option tcplog\n', 2, 'negation_not_supported'],
  ['stats socket keyword', 'global\n    stats socket /run/h.sock mode 660 levle admin\n', 2, 'unknown_bind_keyword'],
];

test('fallback linter rejects what haproxy -c rejects (mirrors the Go table)', () => {
  for (const [name, cfg, line, code, column, end] of STRICT_CASES) {
    const result = lintConfig(cfg);
    assert.equal(result.valid, false, name);
    const found = result.issues.find((i) => i.line === line && i.code === code);
    assert.ok(found, `${name}: want ${line}:${code}, got ${JSON.stringify(result.issues.map((i) => `${i.line}:${i.code}`))}`);
    assert.equal(found.severity, 'error', name);
    if (column) assert.deepEqual([found.column, found.end_column], [column, end], `${name}: ${found.message}`);
  }
  assert.equal(lintConfig('defaults\n    option tcplog,\n').issues[0].message, 'Неизвестная опция «tcplog,» — лишний символ «,»');
});

test('fallback linter accepts valid HAProxy 3.x syntax', () => {
  const cfg = [
    'global',
    '    log stdout format raw local0 info',
    '    log 127.0.0.1:514 len 2048 local1 notice err',
    '    no busy-polling',
    '    stats socket ipv4@127.0.0.1:9999 level admin expose-fd listeners',
    '    tune.ssl.default-dh-param 2048',
    '    ssl-default-bind-options ssl-min-ver TLSv1.2 no-tls-tickets',
    '    set-var proc.x str(a,b)',
    '    cpu-map auto:1/1-4 0-3',
    'defaults named',
    '    mode http',
    '    option httplog clf',
    '    no option http-server-close',
    '    default option redispatch',
    '    option forwardfor except 127.0.0.0/8 header X-Real-IP',
    '    option httpchk GET /health',
    '    option redispatch 1',
    '    tcp-request inspect-delay 5s',
    '    http-request set-header X-A "a, b;" if { path_beg /a, }',
    '    log-format "%ci:%cp [%tr] %ft %b/%s %TR/%Tw/%Tc/%Tr/%Ta %ST %B %CC %CS %tsc %ac/%fc/%bc/%sc/%rc %sq/%bq %hr %hs %{+Q}r"',
    '    timeout http-request 10s',
    '    timeout http-keep-alive 2s',
    '    balance random(2)',
    '    hash-type map-based crc32',
    '    no log',
    'frontend web from named',
    '    bind :443 ssl crt /etc/ssl/a.pem alpn h2,http/1.1 ssl-min-ver TLSv1.2 accept-proxy',
    '    bind quic4@:443 ssl crt /etc/ssl/a.pem alpn h3 thread 1-2 shards by-thread',
    '    bind unix@/run/x.sock mode 600 user haproxy group haproxy',
    '    capture request header Host len 64',
    '    rate-limit sessions 100',
    '    stats uri /stats',
    '    stick-table type ip size 1m expire 10m store http_req_rate(10s)',
    '    tcp-request connection track-sc0 src',
    '    use_backend app if { hdr(host) -i a.example.com }',
    '    default_backend app',
    'backend app',
    '    balance hdr(host)',
    '    hash-type consistent sdbm avalanche',
    '    http-check send meth GET uri /health ver HTTP/1.1 hdr Host a',
    '    http-check expect status 200',
    '    stick on src',
    '    default-server inter 3s fall 3 rise 2 check-sni a.example.com',
    '    server s1 10.0.0.1:443 ssl verify none sni str(a) check weight 100 maxconn 50 source 10.0.0.9 usesrc clientip',
    '    server s2 10.0.0.2:443 check agent-check agent-port 8080 agent-inter 5s on-marked-down shutdown-sessions',
    '    server s3 10.0.0.3:443 set-proxy-v2-tlv-fmt(0x20) %[fc_pp_tlv(0x20)] send-proxy-v2 proxy-v2-options ssl,cert-cn',
    '    server s4 10.0.0.4:80 track app/s1 cookie s4 observe layer4 error-limit 10 on-error mark-down',
    '    server-template srv 1-3 pool.example.com:443 resolvers dns resolve-prefer ipv4 init-addr none',
    'listen stats',
    '    bind 127.0.0.1:8404',
    '    mode http',
    '    stats enable',
    '    stats refresh 10s',
    'resolvers dns',
    '    nameserver ns1 1.1.1.1:53',
    '    accepted_payload_size 8192',
    '',
  ].join('\n');
  const lines = cfg.split('\n');
  assert.deepEqual(lintConfig(cfg).issues.map((i) => `${i.line}:${i.code} ${lines[i.line - 1]}`), []);
});

test('hover never throws on unknown or malformed options', () => {
  const lines = ['defaults', '    option tcplog,', '    option frobnicate', '    no option', 'backend b', '    server s 1.2.3.4:1 chek, weight', '    bind'];
  lines.forEach((line, index) => {
    for (let column = 0; column <= line.length; column++) resolveHover(lines, index, column);
  });
});
