import { execFileSync } from "node:child_process";
import net from "node:net";

const [, , panelURLValue, nodeID] = process.argv;
let panelToken = process.env.PANEL_TOKEN || "";
delete process.env.PANEL_TOKEN;

const nodeSSH = process.env.NODEFLOW_NODE_SSH || "";
const sshIdentity = process.env.NODEFLOW_SSH_IDENTITY || `${process.env.HOME}/.ssh/id_ed25519`;
const smokePort = Number(process.env.NODEFLOW_SMOKE_PORT || "19066");
const quotaBytes = Number(process.env.NODEFLOW_SMOKE_QUOTA_BYTES || "1024");
const trafficChunkBytes = Number(process.env.NODEFLOW_SMOKE_TRAFFIC_CHUNK_BYTES || "4096");
const smokeTimeout = Number(process.env.NODEFLOW_SMOKE_TIMEOUT_MS || "120000");
const smokeName = process.env.NODEFLOW_SMOKE_ROUTE_NAME
  || `nodeflow-quota-smoke-${smokePort}-${process.pid}-${Date.now().toString(36)}`;

if (!panelURLValue || !nodeID || !panelToken) {
  throw new Error(
    "usage: PANEL_TOKEN=... NODEFLOW_SMOKE_CONFIRM_FIREWALL=1 node scripts/live-quota-firewall-smoke.mjs panel-url node-id",
  );
}
if (process.env.NODEFLOW_SMOKE_CONFIRM_FIREWALL !== "1") {
  throw new Error("set NODEFLOW_SMOKE_CONFIRM_FIREWALL=1 to allow temporary NodeFlow-managed UFW apply mode");
}
if (!Number.isInteger(smokePort) || smokePort < 1024 || smokePort > 65535) {
  throw new Error("NODEFLOW_SMOKE_PORT must be an integer between 1024 and 65535");
}
if (!Number.isSafeInteger(quotaBytes) || quotaBytes < 1) {
  throw new Error("NODEFLOW_SMOKE_QUOTA_BYTES must be a positive safe integer");
}
if (!Number.isSafeInteger(trafficChunkBytes) || trafficChunkBytes < 1 || trafficChunkBytes > 1_048_576) {
  throw new Error("NODEFLOW_SMOKE_TRAFFIC_CHUNK_BYTES must be between 1 and 1048576");
}
if (!Number.isSafeInteger(smokeTimeout) || smokeTimeout < 30_000 || smokeTimeout > 600_000) {
  throw new Error("NODEFLOW_SMOKE_TIMEOUT_MS must be between 30000 and 600000");
}
if (!/^[a-zA-Z0-9._-]+@[a-zA-Z0-9.:[\]_-]+$/.test(nodeSSH)) {
  throw new Error("NODEFLOW_NODE_SSH must be an explicit user@host target");
}

const panelURL = new URL("/", panelURLValue);
panelURL.search = "";
panelURL.hash = "";
const apiRoot = new URL("/api/v1/", panelURL);
const nodePath = `nodes/${encodeURIComponent(nodeID)}`;
const routeCollectionPath = `${nodePath}/routes`;
const delay = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds));

let publicHost = process.env.NODEFLOW_SMOKE_PUBLIC_HOST || "";
let createdRoute = null;
let createAttempted = false;
let heldConnection = null;
let initialFirewallMode = null;
let firewallChangeAttempted = false;
let cleanupPromise = null;
let interrupted = false;

const apiRequest = async (path, { method = "GET", body, expected = [200] } = {}) => {
  const response = await fetch(new URL(path, apiRoot), {
    method,
    headers: {
      Authorization: `Bearer ${panelToken}`,
      ...(body === undefined ? {} : { "Content-Type": "application/json" }),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: AbortSignal.timeout(15_000),
  });
  const raw = await response.text();
  let payload = null;
  if (raw) {
    try {
      payload = JSON.parse(raw);
    } catch {
      payload = { message: raw.slice(0, 300) };
    }
  }
  if (!expected.includes(response.status)) {
    const message = payload?.error?.message || payload?.message || response.statusText;
    throw new Error(`${method} ${path} returned HTTP ${response.status}: ${message}`);
  }
  return { status: response.status, payload };
};

const ssh = (remoteCommand) => execFileSync("ssh", [
  "-o", "BatchMode=yes",
  "-o", "IdentitiesOnly=yes",
  "-o", "StrictHostKeyChecking=yes",
  "-o", "ConnectTimeout=8",
  "-o", "ServerAliveInterval=5",
  "-o", "ServerAliveCountMax=2",
  "-i", sshIdentity,
  nodeSSH,
  remoteCommand,
], {
  encoding: "utf8",
  stdio: ["ignore", "pipe", "pipe"],
  timeout: 15_000,
  env: process.env,
}).trim();

const servicesState = () => ssh("systemctl is-active haproxy nodeflow-node-agent | paste -sd, -");
const haproxyPID = () => ssh("systemctl show -p MainPID --value haproxy");
const validateHAProxy = () => ssh(
  "if [ -r /etc/haproxy/haproxy.cfg ]; then "
    + "haproxy -c -f /etc/haproxy/haproxy.cfg >/dev/null 2>&1; "
    + "else curl -fsS http://127.0.0.1:4200/v1/health | grep -q '\"status\":\"ok\"'; fi "
    + "&& printf valid",
);
const remoteListenerOpen = () => ssh(`ss -Hln 'sport = :${smokePort}' || true`) !== "";

const routePayload = (route, enabled) => ({
  expected_version: route.version,
  name: route.name,
  listener_ip: route.listener_ip,
  listener_port: route.listener_port,
  match_mode: route.match_mode,
  snis: route.snis || [],
  fallback: Boolean(route.fallback),
  target_type: route.target_type,
  target_host: route.target_host || "",
  target_port: route.target_port || 0,
  unix_socket_path: route.unix_socket_path || "",
  health_check: Boolean(route.health_check),
  proxy_protocol: route.proxy_protocol || "none",
  quota_bytes: route.quota_bytes ?? null,
  quota_action: route.quota_action || "observe",
  quota_period: route.quota_period || "calendar_month",
  enabled,
  custom_fragment: route.custom_fragment || "",
});

const getRoute = async (routeID, allowMissing = false) => {
  const result = await apiRequest(
    `${routeCollectionPath}/${encodeURIComponent(routeID)}`,
    { expected: allowMissing ? [200, 404] : [200] },
  );
  return result.status === 404 ? null : result.payload;
};

const listRoutes = async () => (await apiRequest(routeCollectionPath)).payload;

const waitForRoute = async (routeID, predicate, label, timeout = smokeTimeout) => {
  const startedAt = Date.now();
  let lastRoute = null;
  while (Date.now() - startedAt < timeout) {
    lastRoute = await getRoute(routeID, true);
    if (predicate(lastRoute)) return lastRoute;
    if (lastRoute?.deployment_state === "failed") {
      throw new Error(`${label} failed: ${lastRoute.deployment_error || "Agent rejected the revision"}`);
    }
    await delay(1_000);
  }
  throw new Error(`${label} timed out; last state=${lastRoute?.deployment_state || "missing"}`);
};

const comparableRoute = (route) => ({
  id: route.id,
  version: route.version,
  name: route.name,
  listener_ip: route.listener_ip,
  listener_port: route.listener_port,
  match_mode: route.match_mode,
  snis: route.snis || [],
  fallback: Boolean(route.fallback),
  target_type: route.target_type,
  target_host: route.target_host || "",
  target_port: route.target_port || 0,
  unix_socket_path: route.unix_socket_path || "",
  health_check: Boolean(route.health_check),
  proxy_protocol: route.proxy_protocol || "none",
  quota_bytes: route.quota_bytes ?? null,
  quota_action: route.quota_action || "observe",
  quota_period: route.quota_period || "calendar_month",
  enabled: Boolean(route.enabled),
  deployed: Boolean(route.deployed),
  deployment_state: route.deployment_state,
  delete_pending: Boolean(route.delete_pending),
  custom_fragment: route.custom_fragment || "",
});

const firewallStatus = async () => {
  const operational = (await apiRequest(`${nodePath}/operational`)).payload;
  return operational?.latest_heartbeat?.metrics?.firewall || null;
};

const waitForFirewall = async (predicate, label, timeout = smokeTimeout) => {
  const startedAt = Date.now();
  let last = null;
  while (Date.now() - startedAt < timeout) {
    last = await firewallStatus();
    if (last?.local_mode && last.local_mode !== "apply") {
      throw new Error(`Node Agent local firewall ceiling is ${last.local_mode}; apply is required for this smoke`);
    }
    if (last?.ufw_available && last.ufw_status !== "active") {
      throw new Error(`UFW is ${last.ufw_status}; it must already be active for this smoke`);
    }
    if (predicate(last)) return last;
    await delay(1_000);
  }
  throw new Error(`${label} timed out; last firewall state=${JSON.stringify(last)}`);
};

const waitForFirewallPolicyPort = async (expected, label, timeout = smokeTimeout) => {
  const startedAt = Date.now();
  let last = null;
  while (Date.now() - startedAt < timeout) {
    last = (await apiRequest(`${nodePath}/firewall`)).payload;
    const hasPort = Array.isArray(last?.tcp_ports) && last.tcp_ports.includes(smokePort);
    if (last?.mode === "apply" && last.plan_complete && hasPort === expected) return last;
    await delay(1_000);
  }
  throw new Error(`${label} timed out; last policy=${JSON.stringify(last)}`);
};

const tcpReachable = (timeout = 1_500) => new Promise((resolve) => {
  const socket = net.createConnection({ host: publicHost, port: smokePort });
  let settled = false;
  const finish = (reachable) => {
    if (settled) return;
    settled = true;
    clearTimeout(timer);
    socket.destroy();
    resolve(reachable);
  };
  const timer = setTimeout(() => finish(false), timeout);
  socket.once("connect", () => finish(true));
  socket.once("error", () => finish(false));
});

const waitPublicReachability = async (expected, label, timeout = 30_000) => {
  const startedAt = Date.now();
  while (Date.now() - startedAt < timeout) {
    if (await tcpReachable() === expected) return;
    await delay(500);
  }
  throw new Error(`${label} timed out for ${publicHost}:${smokePort}`);
};

const connectSSHBanner = (timeout = 5_000) => new Promise((resolve, reject) => {
  const socket = net.createConnection({ host: publicHost, port: smokePort });
  const connection = { socket, banner: "", closed: false };
  let settled = false;
  let received = Buffer.alloc(0);
  const finishError = (error) => {
    if (settled) return;
    settled = true;
    clearTimeout(timer);
    socket.destroy();
    reject(error);
  };
  const timer = setTimeout(
    () => finishError(new Error(`SSH banner timeout from ${publicHost}:${smokePort}`)),
    timeout,
  );
  socket.on("close", () => {
    connection.closed = true;
    if (!settled) finishError(new Error("connection closed before SSH banner"));
  });
  socket.on("error", (error) => {
    connection.closed = true;
    if (!settled) finishError(error);
  });
  socket.on("data", (chunk) => {
    if (settled) return;
    received = Buffer.concat([received, chunk]).subarray(0, 4_096);
    const newline = received.indexOf(0x0a);
    if (newline < 0) return;
    const banner = received.subarray(0, newline + 1).toString("utf8").trim();
    if (!banner.startsWith("SSH-")) {
      finishError(new Error(`unexpected target banner: ${banner.slice(0, 80)}`));
      return;
    }
    settled = true;
    clearTimeout(timer);
    connection.banner = banner;
    resolve(connection);
  });
});

const generateTrafficConnection = async () => {
  let connection;
  try {
    connection = await connectSSHBanner(3_000);
  } catch {
    return false;
  }
  connection.socket.write(Buffer.from("SSH-2.0-OpenSSH_9.9\r\n"));
  connection.socket.write(Buffer.alloc(trafficChunkBytes, 0x41));
  await delay(75);
  connection.socket.destroy();
  return true;
};

const newConnectionIsBlocked = async () => {
  try {
    const connection = await connectSSHBanner(2_000);
    connection.socket.destroy();
    return false;
  } catch {
    return remoteListenerOpen();
  }
};

const proveHeldConnectionAlive = (connection, timeout = 5_000) => new Promise((resolve, reject) => {
  if (!connection || connection.closed || connection.socket.destroyed) {
    reject(new Error("pre-existing SSH connection closed before quota enforcement"));
    return;
  }
  const socket = connection.socket;
  let settled = false;
  const finish = (error, bytes = 0) => {
    if (settled) return;
    settled = true;
    clearTimeout(timer);
    socket.off("data", onData);
    socket.off("close", onClose);
    socket.off("error", onError);
    if (error) reject(error);
    else resolve(bytes);
  };
  const onData = (chunk) => finish(null, chunk.length);
  const onClose = () => finish(new Error("pre-existing SSH connection closed during proof"));
  const onError = (error) => finish(error);
  const timer = setTimeout(() => finish(new Error("pre-existing SSH connection produced no KEX response")), timeout);
  socket.once("data", onData);
  socket.once("close", onClose);
  socket.once("error", onError);
  socket.write(Buffer.from("SSH-2.0-OpenSSH_9.9\r\n"));
});

const waitForQuotaBlock = async (routeID, timeout = smokeTimeout) => {
  const startedAt = Date.now();
  let last = null;
  let nextBurstAt = 0;
  while (Date.now() - startedAt < timeout) {
    const report = (await apiRequest(`${nodePath}/traffic`)).payload;
    last = report?.routes?.find((route) => route.route_id === routeID) || null;
    if (
      last?.observed
      && last.applied
      && last.enforcement
      && last.reached
      && last.block_requested
      && last.blocked
    ) {
      const status = await firewallStatus();
      if (
        status?.effective_mode === "apply"
        && status.ufw_status === "active"
        && status.managed_tcp_ports?.includes(smokePort)
        && await newConnectionIsBlocked()
      ) {
        return last;
      }
    }
    if (!last?.reached && Date.now() >= nextBurstAt) {
      for (let attempt = 0; attempt < 3; attempt += 1) {
        if (!await generateTrafficConnection()) break;
      }
      nextBurstAt = Date.now() + 2_000;
    }
    await delay(1_000);
  }
  throw new Error(`quota block timed out; last traffic state=${JSON.stringify(last)}`);
};

const discoverCreatedRoute = async () => {
  if (createdRoute?.id) return getRoute(createdRoute.id, true);
  if (!createAttempted) return null;
  for (let attempt = 0; attempt < 10; attempt += 1) {
    const routes = await listRoutes();
    const route = routes.find((candidate) => (
      candidate.name === smokeName
      && candidate.listener_ip === "0.0.0.0"
      && candidate.listener_port === smokePort
    ));
    if (route) return route;
    await delay(500);
  }
  return null;
};

const cleanup = async () => {
  if (cleanupPromise) return cleanupPromise;
  cleanupPromise = (async () => {
    const failures = [];
    const attempt = async (label, operation) => {
      try {
        await operation();
      } catch (error) {
        failures.push(`${label}: ${error.message}`);
      }
    };
    if (heldConnection?.socket) heldConnection.socket.destroy();
    heldConnection = null;

    let route = null;
    await attempt("discover smoke route", async () => { route = await discoverCreatedRoute(); });
    if (route) {
      await attempt("disable smoke route", async () => {
        if (route.enabled || route.deployed || route.delete_pending || !["draft", "disabled"].includes(route.deployment_state)) {
          try {
            route = (await apiRequest(`${routeCollectionPath}/${encodeURIComponent(route.id)}`, {
              method: "PUT",
              body: routePayload(route, false),
              expected: [200],
            })).payload;
          } catch {
            route = await getRoute(route.id, true);
          }
          if (route) {
            route = await waitForRoute(
              route.id,
              (value) => value === null || (
                !value.enabled && !value.deployed && ["draft", "disabled"].includes(value.deployment_state)
              ),
              "cleanup disable",
            );
          }
        }
      });
      await attempt("close public smoke listener", async () => {
        await waitPublicReachability(false, "cleanup public listener close");
      });
      await attempt("remove managed UFW port", async () => {
        await waitForFirewallPolicyPort(false, "cleanup firewall plan");
        await waitForFirewall(
          (status) => status?.effective_mode === "apply"
            && status.ufw_status === "active"
            && !status.managed_tcp_ports?.includes(smokePort),
          "cleanup managed UFW port removal",
        );
      });
      await attempt("delete smoke route", async () => {
        if (!route) return;
        const result = await apiRequest(
          `${routeCollectionPath}/${encodeURIComponent(route.id)}?expected_version=${route.version}`,
          { method: "DELETE", expected: [202, 204] },
        );
        if (result.status === 202) {
          await waitForRoute(route.id, (value) => value === null, "cleanup delete");
        }
      });
    }
    createdRoute = null;

    if (initialFirewallMode && firewallChangeAttempted) {
      await attempt("restore firewall policy", async () => {
        const current = (await apiRequest(`${nodePath}/firewall`)).payload;
        if (current.mode !== initialFirewallMode) {
          await apiRequest(`${nodePath}/firewall`, {
            method: "PUT",
            body: { mode: initialFirewallMode },
            expected: [200],
          });
        }
        const restored = (await apiRequest(`${nodePath}/firewall`)).payload;
        if (restored.mode !== initialFirewallMode) {
          throw new Error(`expected ${initialFirewallMode}, got ${restored.mode}`);
        }
      });
    }
    if (failures.length) throw new Error(failures.join("; "));
  })();
  return cleanupPromise;
};

const signalExit = async (signal) => {
  if (interrupted) return;
  interrupted = true;
  try {
    await cleanup();
  } catch (error) {
    process.stderr.write(`cleanup after ${signal} failed: ${error.message}\n`);
  } finally {
    panelToken = "";
    process.exit(signal === "SIGINT" ? 130 : 143);
  }
};
process.once("SIGINT", () => { void signalExit("SIGINT"); });
process.once("SIGTERM", () => { void signalExit("SIGTERM"); });

let passed = false;
let primaryError = null;
let initialRoutes = [];
let initialPID = "";

try {
  const node = (await apiRequest(nodePath)).payload;
  publicHost = publicHost || node?.address || "";
  if (!publicHost || /[/?#]/.test(publicHost)) {
    throw new Error("node address is not a plain public host; set NODEFLOW_SMOKE_PUBLIC_HOST");
  }

  initialRoutes = await listRoutes();
  if (!Array.isArray(initialRoutes)) throw new Error("routes API did not return an array");
  if (initialRoutes.some((route) => route.delete_pending || ["pending", "applying", "failed"].includes(route.deployment_state))) {
    throw new Error("a pre-existing route is not stable; finish its deployment before running the smoke");
  }
  if (initialRoutes.some((route) => route.name === smokeName || route.listener_port === smokePort)) {
    throw new Error(`smoke route name or listener port ${smokePort} already exists`);
  }
  if (await tcpReachable()) throw new Error(`public port ${publicHost}:${smokePort} is already reachable`);
  if (remoteListenerOpen()) throw new Error(`node already listens on TCP ${smokePort}`);
  if (servicesState() !== "active,active") throw new Error("HAProxy or Node Agent is not active before smoke");
  if (validateHAProxy() !== "valid") throw new Error("HAProxy config is invalid before smoke");
  initialPID = haproxyPID();
  if (!/^[1-9][0-9]*$/.test(initialPID)) throw new Error("HAProxy MainPID is invalid");

  const initialFirewall = (await apiRequest(`${nodePath}/firewall`)).payload;
  initialFirewallMode = initialFirewall.mode;
  if (initialFirewallMode !== "apply") {
    firewallChangeAttempted = true;
    await apiRequest(`${nodePath}/firewall`, {
      method: "PUT",
      body: { mode: "apply" },
      expected: [200],
    });
  }
  await waitForFirewall(
    (status) => status?.local_mode === "apply"
      && status.requested_mode === "apply"
      && status.effective_mode === "apply"
      && status.ufw_status === "active",
    "firewall apply readiness",
  );

  createAttempted = true;
  createdRoute = (await apiRequest(routeCollectionPath, {
    method: "POST",
    expected: [201],
    body: {
      name: smokeName,
      listener_ip: "0.0.0.0",
      listener_port: smokePort,
      match_mode: "any_tcp",
      snis: [],
      fallback: true,
      target_type: "tcp",
      target_host: "127.0.0.1",
      target_port: 22,
      health_check: true,
      proxy_protocol: "none",
      quota_bytes: quotaBytes,
      quota_action: "block_new",
      quota_period: "hourly",
      enabled: false,
      custom_fragment: "",
    },
  })).payload;
  if (
    createdRoute.enabled
    || createdRoute.deployed
    || createdRoute.deployment_state !== "draft"
    || createdRoute.listener_ip !== "0.0.0.0"
    || createdRoute.target_host !== "127.0.0.1"
    || createdRoute.target_port !== 22
  ) {
    throw new Error("created route is not the expected isolated disabled draft");
  }
  if (await tcpReachable() || remoteListenerOpen()) {
    throw new Error("disabled draft unexpectedly opened its public listener");
  }

  createdRoute = (await apiRequest(`${routeCollectionPath}/${encodeURIComponent(createdRoute.id)}`, {
    method: "PUT",
    body: routePayload(createdRoute, true),
    expected: [200],
  })).payload;
  createdRoute = await waitForRoute(
    createdRoute.id,
    (route) => route?.enabled && route.deployed && route.deployment_state === "active",
    "route enable/apply",
  );
  await waitForFirewallPolicyPort(true, "active firewall plan");
  await waitForFirewall(
    (status) => status?.effective_mode === "apply"
      && status.ufw_status === "active"
      && status.managed_tcp_ports?.includes(smokePort),
    "managed UFW port open",
  );
  await waitPublicReachability(true, "public listener open");
  heldConnection = await connectSSHBanner();

  for (let attempt = 0; attempt < 4; attempt += 1) {
    if (!await generateTrafficConnection()) break;
  }
  const quotaState = await waitForQuotaBlock(createdRoute.id);
  const heldProofBytes = await proveHeldConnectionAlive(heldConnection);
  heldConnection.socket.destroy();
  heldConnection = null;

  createdRoute = (await apiRequest(`${routeCollectionPath}/${encodeURIComponent(createdRoute.id)}`, {
    method: "PUT",
    body: routePayload(createdRoute, false),
    expected: [200],
  })).payload;
  createdRoute = await waitForRoute(
    createdRoute.id,
    (route) => route && !route.enabled && !route.deployed && route.deployment_state === "disabled",
    "route disable/apply",
  );
  await waitPublicReachability(false, "public listener close");
  await waitForFirewallPolicyPort(false, "disabled firewall plan");
  await waitForFirewall(
    (status) => status?.effective_mode === "apply"
      && status.ufw_status === "active"
      && !status.managed_tcp_ports?.includes(smokePort),
    "managed UFW port close",
  );

  const deleteResult = await apiRequest(
    `${routeCollectionPath}/${encodeURIComponent(createdRoute.id)}?expected_version=${createdRoute.version}`,
    { method: "DELETE", expected: [202, 204] },
  );
  if (deleteResult.status === 202) {
    await waitForRoute(createdRoute.id, (route) => route === null, "route delete");
  }
  createdRoute = null;
  createAttempted = false;

  if (initialFirewallMode !== "apply") {
    await apiRequest(`${nodePath}/firewall`, {
      method: "PUT",
      body: { mode: initialFirewallMode },
      expected: [200],
    });
  }
  const restoredFirewall = (await apiRequest(`${nodePath}/firewall`)).payload;
  if (restoredFirewall.mode !== initialFirewallMode) {
    throw new Error(`firewall policy was not restored to ${initialFirewallMode}`);
  }

  const finalRoutes = await listRoutes();
  const initialComparable = initialRoutes.map(comparableRoute).sort((a, b) => a.id.localeCompare(b.id));
  const finalComparable = finalRoutes.map(comparableRoute).sort((a, b) => a.id.localeCompare(b.id));
  if (JSON.stringify(finalComparable) !== JSON.stringify(initialComparable)) {
    throw new Error("pre-existing route specifications changed during smoke");
  }
  if (await tcpReachable() || remoteListenerOpen()) throw new Error("smoke listener remains open after delete");
  if (servicesState() !== "active,active") throw new Error("HAProxy or Node Agent is not active after smoke");
  if (validateHAProxy() !== "valid") throw new Error("HAProxy config is invalid after smoke");
  const finalPID = haproxyPID();
  if (!/^[1-9][0-9]*$/.test(finalPID)) throw new Error("HAProxy MainPID is invalid after smoke");

  passed = true;
  console.log(
    `PASS quota/firewall smoke: ${publicHost}:${smokePort} opened and served SSH; `
    + `hourly quota ${quotaBytes} blocked new connections at ${quotaState.quota_used_bytes} bytes; `
    + `pre-existing connection returned ${heldProofBytes} post-block bytes; UFW policy restored; `
    + `existing routes unchanged; HAProxy healthy (PID ${initialPID} -> ${finalPID})`,
  );
} catch (error) {
  primaryError = error;
} finally {
  if (!passed) {
    try {
      await cleanup();
    } catch (cleanupError) {
      primaryError = primaryError
        ? new Error(`${primaryError.message}; cleanup failed: ${cleanupError.message}`)
        : cleanupError;
    }
  }
  panelToken = "";
}

if (primaryError) throw primaryError;
