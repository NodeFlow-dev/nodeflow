import { test } from 'node:test';
import assert from 'node:assert/strict';
import { model } from './routeMatrix.ts';

type RouteDraft = import('../src/features/routes/model.ts').RouteDraft;

// Each value below was accepted by the editor and then rejected by the Go
// validator (internal/panel/route_validation.go) with a 400 on save.

function draft(): RouteDraft {
  const d = model.emptyRouteDraft();
  d.name = 'edge';
  d.snis = ['app.example.com'];
  d.servers = [
    { ...model.emptyServerDraft('srv1'), _key: 'a', host: '192.0.2.10', port: 443 },
    { ...model.emptyServerDraft('srv2'), _key: 'b', host: 'pool.example.com', port: 443, dnsPool: true },
  ];
  return d;
}

const errors = (d: RouteDraft) => model.validateRouteDraft(d, []).map((e) => e.message);

test('IP addresses follow Go net.ParseIP', () => {
  for (const ip of ['192.0.2.1', '0.0.0.0', '::', '::1', '2001:db8::1', '2001:DB8:0:0:0:0:0:1', '1:2:3:4:5:6:7::', '::1:2:3:4:5:6:7', 'fe80::1']) {
    assert.equal(model.isIPAddress(ip), true, ip);
  }
  // net.ParseIP: "listener_ip must be * or an IP address", "servers[0].preferred_ip must be a valid IP address", ...
  for (const ip of ['01.2.3.4', '1.2.3.04', '1:::2', '1::2::3', ':::', '1:2:3:4:5:6:7:8:9', '1:2:3:4:5:6:7', '1::2:3:4:5:6:7:8', '12345::', '::g']) {
    assert.equal(model.isIPAddress(ip), false, ip);
  }
});

test('host names with a numeric last label are not DNS names', () => {
  // validDNSName: "servers[0] TCP entry requires a valid host and port", "hostname must be a valid DNS name".
  for (const host of ['backend.1', 'host.0x1f', 'a.b.123']) {
    assert.equal(model.isValidTargetHost(host), false, host);
    const d = draft();
    d.snis = [host];
    assert.ok(errors(d).some((m) => m.includes('SNI')), host);
  }
  for (const host of ['a.b1', 'x1.example', '0x.example', 'host.0xg', 'example.com.']) assert.equal(model.isValidTargetHost(host), true, host);
});

test('accept_proxy_from rejects forms normalizeAcceptProxyCIDR refuses', () => {
  for (const entry of ['10.0.0.0/0', '1.2.3.4/0', '2001:db8::/0', '::ffff:0:0/96', '::ffff:10.0.0.0/104', '01.0.0.0/8', 'backend.1']) {
    assert.equal(model.isValidTrustedProxyEntry(entry), false, entry);
  }
  for (const entry of ['0.0.0.0/0', '::/0', '10.0.0.0/8', '10.0.0.1/8', '2001:db8::/32', 'proxy.example.com']) {
    assert.equal(model.isValidTrustedProxyEntry(entry), true, entry);
  }
});

test('per-IP weights: the same address in two spellings is a duplicate', () => {
  // "servers[1].ip_weights[1].ip "2001:db8::1" is a duplicate"
  const d = draft();
  d.servers[1].ipWeights = [{ ip: '2001:db8::1', weight: 2, cost: '' }, { ip: '2001:DB8:0::1', weight: 3, cost: '' }];
  assert.ok(errors(d).some((m) => m.includes('IP не должны повторяться')));
});

test('failover: a DNS-pool reserve must be the only reserve', () => {
  // "in failover mode a DNS-pool reserve must be the only reserve"
  const d = draft();
  d.balanceMode = 'failover';
  d.servers = model.withCanonicalFailoverRoles([...d.servers, { ...d.servers[0], _key: 'c', name: 'srv3', host: '192.0.2.30' }]);
  assert.ok(errors(d).some((m) => m.includes('единственным резервным')));

  const pref = draft();
  pref.balanceMode = 'failover';
  pref.servers = model.withCanonicalFailoverRoles([{ ...pref.servers[1], preferredIP: '192.0.2.50' }, pref.servers[0]]);
  assert.ok(errors(pref).some((m) => m.includes('основным IP')));

  const ok = draft();
  ok.balanceMode = 'failover';
  ok.servers = model.withCanonicalFailoverRoles(ok.servers);
  assert.deepEqual(errors(ok), []);
});

test('server names must not collide with DNS-pool slot names', () => {
  // "servers name "srv2_10" collides with DNS-pool server "srv2" slot names"
  const d = draft();
  d.servers[0].name = 'srv2_10';
  assert.ok(errors(d).some((m) => m.includes('DNS-пула srv2')));
  d.servers[0].name = 'srv2_x';
  assert.deepEqual(errors(d), []);
});

test('expert layer cannot override balancing of a DNS-pool route', () => {
  // "custom_fragment cannot override DNS pool server or balancing directives"
  for (const line of ['balance roundrobin', '  server extra 192.0.2.99:443', 'default-server inter 3s', 'HASH-TYPE consistent', 'server-template x 2 a.example.com:443']) {
    const d = draft();
    d.expertOverride = `timeout connect 5s\n${line}`;
    assert.ok(errors(d).some((m) => m.includes('DNS-пулом')), line);
    d.servers = [d.servers[0]];
    assert.deepEqual(errors(d), [], `${line} without DNS pool`);
  }
});

test('expert layer line limit counts the directive, not its indentation', () => {
  const d = draft();
  d.servers = [d.servers[0]];
  d.expertOverride = `    ${'a'.repeat(512)}`; // stored fragments are indented by four spaces
  assert.deepEqual(errors(d), []);
  d.expertOverride = 'a'.repeat(513);
  assert.ok(errors(d).some((m) => m.includes('512')));
});

test('route name must not contain control characters', () => {
  // "route fields cannot contain control characters"
  const d = draft();
  d.name = 'edge\tmsk';
  assert.ok(errors(d).some((m) => m.includes('управляющие символы')));
  d.name = 'edge msk';
  assert.deepEqual(errors(d), []);
});
