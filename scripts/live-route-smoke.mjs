import { execFileSync } from "node:child_process";

const [, , panelURLValue, nodeID] = process.argv;
let panelToken = process.env.PANEL_TOKEN || "";
delete process.env.PANEL_TOKEN;

const nodeSSH = process.env.NODEFLOW_NODE_SSH || "";
const sshIdentity = process.env.NODEFLOW_SSH_IDENTITY || `${process.env.HOME}/.ssh/id_ed25519`;
const smokePort = Number(process.env.NODEFLOW_SMOKE_PORT || "19065");
const smokeName = process.env.NODEFLOW_SMOKE_ROUTE_NAME || `nodeflow-live-smoke-${smokePort}`;

if (!panelURLValue || !nodeID || !panelToken) {
  throw new Error(
    "usage: PANEL_TOKEN=... node scripts/live-route-smoke.mjs panel-url node-id",
  );
}
if (!Number.isInteger(smokePort) || smokePort < 1024 || smokePort > 65535) {
  throw new Error("NODEFLOW_SMOKE_PORT must be an integer between 1024 and 65535");
}
if (!/^[a-zA-Z0-9._-]+@[a-zA-Z0-9.:_-]+$/.test(nodeSSH)) {
  throw new Error("NODEFLOW_NODE_SSH must be an explicit user@host target");
}

const panelURL = new URL("/", panelURLValue);
panelURL.search = "";
panelURL.hash = "";
const apiRoot = new URL("/api/v1/", panelURL);
const routeCollectionPath = `nodes/${encodeURIComponent(nodeID)}/routes`;
const delay = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds));

let createdRoute = null;
let createAttempted = false;
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

const haproxyPID = () => ssh("systemctl show -p MainPID --value haproxy");
const servicesState = () => ssh(
  "systemctl is-active haproxy nodeflow-node-agent | paste -sd, -",
);
const listenerLines = () => ssh(`ss -Hln 'sport = :${smokePort}' || true`);
const loopbackListenerOpen = () => {
  const output = listenerLines();
  if (!output) return false;
  return output.split("\n").some((line) => (
    line.includes(`127.0.0.1:${smokePort}`) || line.includes(`[::ffff:127.0.0.1]:${smokePort}`)
  ));
};

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

const listRoutes = async () => (
  await apiRequest(routeCollectionPath)
).payload;

const waitForRoute = async (routeID, predicate, label, timeout = 60_000) => {
  const startedAt = Date.now();
  let lastRoute = null;
  while (Date.now() - startedAt < timeout) {
    lastRoute = await getRoute(routeID, true);
    if (predicate(lastRoute)) return lastRoute;
    if (lastRoute?.deployment_state === "failed") {
      throw new Error(
        `${label} failed: ${lastRoute.deployment_error || "Agent rejected the revision"}`,
      );
    }
    await delay(1_000);
  }
  throw new Error(
    `${label} timed out; last state=${lastRoute?.deployment_state || "missing"}`,
  );
};

const waitForListener = async (expectedOpen, label, timeout = 20_000) => {
  const startedAt = Date.now();
  while (Date.now() - startedAt < timeout) {
    if (loopbackListenerOpen() === expectedOpen) return;
    await delay(500);
  }
  throw new Error(`${label} timed out`);
};

const comparableRoute = (route) => ({
  id: route.id,
  version: route.version,
  name: route.name,
  listener_ip: route.listener_ip,
  listener_port: route.listener_port,
  match_mode: route.match_mode,
  snis: route.snis || [],
  target_type: route.target_type,
  target_host: route.target_host || "",
  target_port: route.target_port || 0,
  unix_socket_path: route.unix_socket_path || "",
  health_check: Boolean(route.health_check),
  proxy_protocol: route.proxy_protocol,
  quota_bytes: route.quota_bytes ?? null,
  quota_action: route.quota_action,
  quota_period: route.quota_period,
  enabled: Boolean(route.enabled),
});

const cleanup = async () => {
  if (cleanupPromise) return cleanupPromise;
  cleanupPromise = (async () => {
    if (!createdRoute?.id && !createAttempted) return;
    try {
      let route = createdRoute?.id ? await getRoute(createdRoute.id, true) : null;
      // The POST can commit even if its response is lost.  Only discover a
      // route after this process attempted the create; a preflight collision
      // must never make cleanup delete a route that was already there.
      for (let attempt = 0; !route && createAttempted && attempt < 10; attempt += 1) {
        const candidates = await listRoutes();
        route = candidates.find((candidate) => (
          candidate.name === smokeName
          && candidate.listener_ip === "127.0.0.1"
          && candidate.listener_port === smokePort
        )) || null;
        if (!route) await delay(500);
      }
      if (!route) return;

      if (route.enabled || route.deployed || route.delete_pending || !["draft", "disabled"].includes(route.deployment_state)) {
        try {
          const disabled = await apiRequest(
            `${routeCollectionPath}/${encodeURIComponent(route.id)}`,
            { method: "PUT", body: routePayload(route, false), expected: [200] },
          );
          route = disabled.payload;
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

      if (route) {
        const result = await apiRequest(
          `${routeCollectionPath}/${encodeURIComponent(route.id)}?expected_version=${route.version}`,
          { method: "DELETE", expected: [202, 204] },
        );
        if (result.status === 202) {
          await waitForRoute(route.id, (value) => value === null, "cleanup delete");
        }
      }
    } finally {
      createdRoute = null;
    }
  })();
  return cleanupPromise;
};

const signalExit = async (signal) => {
  if (interrupted) return;
  interrupted = true;
  try {
    await cleanup();
    await waitForListener(false, "signal cleanup listener close");
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
  initialRoutes = await listRoutes();
  if (!Array.isArray(initialRoutes)) throw new Error("routes API did not return an array");
  if (initialRoutes.some((route) => route.name === smokeName || (
    route.listener_ip === "127.0.0.1" && route.listener_port === smokePort
  ))) {
    throw new Error(`smoke route or listener 127.0.0.1:${smokePort} already exists`);
  }
  if (loopbackListenerOpen()) {
    throw new Error(`listener 127.0.0.1:${smokePort} is already open`);
  }
  if (servicesState() !== "active,active") {
    throw new Error("HAProxy or Node Agent is not active before smoke");
  }
  initialPID = haproxyPID();
  if (!/^[1-9][0-9]*$/.test(initialPID)) throw new Error("HAProxy MainPID is invalid");

  createAttempted = true;
  const created = await apiRequest(routeCollectionPath, {
    method: "POST",
    expected: [201],
    body: {
      name: smokeName,
      listener_ip: "127.0.0.1",
      listener_port: smokePort,
      match_mode: "destination_ip",
      snis: [],
      fallback: true,
      target_type: "tcp",
      target_host: process.env.NODEFLOW_SMOKE_TARGET_HOST || "192.0.2.1",
      target_port: 443,
      health_check: false,
      proxy_protocol: "none",
      quota_bytes: null,
      quota_action: "observe",
      quota_period: "calendar_month",
      enabled: false,
      custom_fragment: "",
    },
  });
  createdRoute = created.payload;
  if (
    createdRoute.enabled
    || createdRoute.deployed
    || createdRoute.deployment_state !== "draft"
    || createdRoute.listener_ip !== "127.0.0.1"
    || createdRoute.match_mode !== "destination_ip"
  ) {
    throw new Error("created route is not an isolated disabled destination-IP draft");
  }
  if (loopbackListenerOpen()) throw new Error("disabled draft unexpectedly opened a listener");

  createdRoute = (
    await apiRequest(`${routeCollectionPath}/${encodeURIComponent(createdRoute.id)}`, {
      method: "PUT",
      body: routePayload(createdRoute, true),
      expected: [200],
    })
  ).payload;
  createdRoute = await waitForRoute(
    createdRoute.id,
    (route) => route?.enabled && route.deployed && route.deployment_state === "active",
    "route enable/apply",
  );
  await waitForListener(true, "loopback listener open");

  createdRoute = (
    await apiRequest(`${routeCollectionPath}/${encodeURIComponent(createdRoute.id)}`, {
      method: "PUT",
      body: routePayload(createdRoute, false),
      expected: [200],
    })
  ).payload;
  createdRoute = await waitForRoute(
    createdRoute.id,
    (route) => route && !route.enabled && !route.deployed && route.deployment_state === "disabled",
    "route disable/apply",
  );
  await waitForListener(false, "loopback listener close");

  const deleteResult = await apiRequest(
    `${routeCollectionPath}/${encodeURIComponent(createdRoute.id)}?expected_version=${createdRoute.version}`,
    { method: "DELETE", expected: [202, 204] },
  );
  if (deleteResult.status === 202) {
    await waitForRoute(createdRoute.id, (route) => route === null, "route delete");
  }
  createdRoute = null;
  createAttempted = false;

  const finalRoutes = await listRoutes();
  const initialComparable = initialRoutes.map(comparableRoute).sort((a, b) => a.id.localeCompare(b.id));
  const finalComparable = finalRoutes.map(comparableRoute).sort((a, b) => a.id.localeCompare(b.id));
  if (JSON.stringify(finalComparable) !== JSON.stringify(initialComparable)) {
    throw new Error("pre-existing route specifications changed during smoke");
  }
  if (loopbackListenerOpen()) throw new Error("smoke listener remains open after delete");
  if (servicesState() !== "active,active") throw new Error("HAProxy or Node Agent is not active after smoke");
  const finalPID = haproxyPID();
  if (finalPID !== initialPID) {
    throw new Error(`HAProxy MainPID changed during route lifecycle (${initialPID} -> ${finalPID})`);
  }

  passed = true;
  console.log(
    `PASS live route CRUD/apply: draft -> active -> disabled -> deleted; `
    + `listener 127.0.0.1:${smokePort} opened/closed; existing routes unchanged; HAProxy PID ${finalPID}`,
  );
} catch (error) {
  primaryError = error;
} finally {
  if (!passed) {
    try {
      await cleanup();
      await waitForListener(false, "final cleanup listener close");
    } catch (cleanupError) {
      if (primaryError) {
        primaryError = new Error(`${primaryError.message}; cleanup failed: ${cleanupError.message}`);
      } else {
        primaryError = cleanupError;
      }
    }
  }
  panelToken = "";
}

if (primaryError) throw primaryError;
