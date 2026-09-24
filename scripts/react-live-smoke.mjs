import fs from "node:fs";
import path from "node:path";

const [, , targetURL, outputDirectory] = process.argv;
const inputURL = targetURL ? new URL(targetURL) : null;
const demoMode = process.env.NODEFLOW_SMOKE_DEMO === "1" || inputURL?.searchParams.get("demo") === "1";
let panelToken = process.env.PANEL_TOKEN || "";
delete process.env.PANEL_TOKEN;

if (!inputURL || !outputDirectory || (!panelToken && !demoMode)) {
  throw new Error(
    "usage: PANEL_TOKEN=... node scripts/react-live-smoke.mjs panel-url output-directory "
    + "or NODEFLOW_SMOKE_DEMO=1 node scripts/react-live-smoke.mjs panel-url output-directory",
  );
}

const cdpURL = new URL(process.env.NODEFLOW_CDP_URL || "http://127.0.0.1:9229");
const origin = new URL("/", inputURL);
origin.search = "";
origin.hash = "";
const delay = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds));
const appURL = (pathname) => {
  const url = new URL(pathname, origin);
  if (demoMode) url.searchParams.set("demo", "1");
  return url.href;
};

fs.mkdirSync(outputDirectory, { recursive: true, mode: 0o700 });
fs.chmodSync(outputDirectory, 0o700);

let pages;
for (let attempt = 0; attempt < 40; attempt += 1) {
  try {
    pages = await fetch(new URL("/json/list", cdpURL)).then((response) => response.json());
    if (pages.length) break;
  } catch {
    // Chromium can need a moment after its DevTools socket starts listening.
  }
  await delay(100);
}
if (!pages?.length) {
  throw new Error("Chromium DevTools endpoint is unavailable at " + cdpURL.origin);
}

const page = pages.find((item) => item.type === "page") || pages[0];
const socket = new WebSocket(page.webSocketDebuggerUrl);
await new Promise((resolve, reject) => {
  socket.addEventListener("open", resolve, { once: true });
  socket.addEventListener("error", reject, { once: true });
});

let sequence = 0;
const pending = new Map();
let fetchPausedHandler = null;
const asyncEventErrors = [];
socket.addEventListener("message", (event) => {
  const message = JSON.parse(event.data);
  if (message.method === "Fetch.requestPaused" && fetchPausedHandler) {
    Promise.resolve(fetchPausedHandler(message.params)).catch((error) => {
      asyncEventErrors.push(error instanceof Error ? error.message : String(error));
    });
    return;
  }
  if (!message.id || !pending.has(message.id)) return;
  const operation = pending.get(message.id);
  pending.delete(message.id);
  if (message.error) operation.reject(new Error(message.error.message));
  else operation.resolve(message.result);
});

const command = (method, params = {}) => new Promise((resolve, reject) => {
  const id = ++sequence;
  pending.set(id, { resolve, reject });
  socket.send(JSON.stringify({ id, method, params }));
});

const evaluate = async (expression) => {
  const response = await command("Runtime.evaluate", {
    expression,
    awaitPromise: true,
    returnByValue: true,
  });
  if (response.exceptionDetails) {
    const description = response.exceptionDetails.exception?.description
      || response.exceptionDetails.text
      || "page evaluation failed";
    throw new Error(description);
  }
  return response.result.value;
};

const viewport = async ({ width, height, mobile }) => {
  await command("Emulation.setDeviceMetricsOverride", {
    width,
    height,
    mobile,
    screenWidth: width,
    screenHeight: height,
    deviceScaleFactor: 1,
  });
};

const waitFor = async (expression, label, timeout = 15000) => {
  const startedAt = Date.now();
  let lastError;
  while (Date.now() - startedAt < timeout) {
    try {
      if (await evaluate("Boolean(" + expression + ")")) return;
    } catch (error) {
      lastError = error;
    }
    await delay(100);
  }
  throw new Error(
    "timeout waiting for " + label + (lastError ? ": " + lastError.message : ""),
  );
};

const navigate = async (pathname, readyExpression, label) => {
  await command("Page.navigate", { url: appURL(pathname) });
  await waitFor("document.readyState === 'complete' || document.readyState === 'interactive'", label + " document");
  await waitFor(readyExpression, label);
  await delay(350);
  await evaluate("(async()=>{if(document.fonts?.ready)await document.fonts.ready;await new Promise((resolve)=>requestAnimationFrame(()=>requestAnimationFrame(resolve)));return true})()");
  await delay(650);
};

const clickSelector = async (selector) => {
  const clicked = await evaluate(
    "(()=>{const element=document.querySelector(" + JSON.stringify(selector)
    + ");if(!element)return false;element.click();return true})()",
  );
  if (!clicked) throw new Error("element not found: " + selector);
  await delay(250);
};

const clickButton = async (label) => {
  const clicked = await evaluate(
    "(()=>{const label=" + JSON.stringify(label)
    + ";const element=[...document.querySelectorAll('button,a')].find((item)=>item.textContent.trim()===label);"
    + "if(!element)return false;element.click();return true})()",
  );
  if (!clicked) throw new Error("button not found: " + label);
  await delay(250);
};

const pressEscape = async () => {
  await command("Input.dispatchKeyEvent", {
    type: "keyDown",
    key: "Escape",
    code: "Escape",
    windowsVirtualKeyCode: 27,
    nativeVirtualKeyCode: 27,
  });
  await command("Input.dispatchKeyEvent", {
    type: "keyUp",
    key: "Escape",
    code: "Escape",
    windowsVirtualKeyCode: 27,
    nativeVirtualKeyCode: 27,
  });
  await delay(250);
};

const screenshot = async (name) => {
  const capture = {
    format: "png",
    captureBeyondViewport: false,
    fromSurface: true,
  };
  // A warm capture flushes Chromium's fixed AppShell compositor layers after
  // cross-route viewport changes; only the second, settled frame is retained.
  await command("Page.captureScreenshot", capture);
  await delay(60);
  const image = await command("Page.captureScreenshot", capture);
  const filename = path.join(outputDirectory, name);
  fs.writeFileSync(filename, Buffer.from(image.data, "base64"), { mode: 0o600 });
  return name;
};

const assertions = [];
const failures = [];
const check = (name, condition, detail = "") => {
  assertions.push({ name, ok: Boolean(condition), detail: condition ? "" : detail });
  if (!condition) failures.push(name + (detail ? ": " + detail : ""));
};

const inspectPage = async (selector, expectedHeading) => evaluate(
  "(()=>{"
  + "const main=document.querySelector(" + JSON.stringify(selector) + ");"
  + "const heading=main?.querySelector('h1')?.textContent.trim()||'';"
  + "const rect=main?.getBoundingClientRect();"
  + "const mobileHeader=document.querySelector('.nf-mobile-header');"
  + "const mobileHeaderRect=mobileHeader?.getBoundingClientRect();"
  + "const mobileLogo=mobileHeader?.querySelector('.nf-logo')?.getBoundingClientRect();"
  + "const mobileBurger=mobileHeader?.querySelector('.mantine-Burger-root')?.getBoundingClientRect();"
  + "const sidebar=document.querySelector('.nf-sidebar');"
  + "const sidebarRect=sidebar?.getBoundingClientRect();"
  + "const brand=sidebar?.querySelector('.nf-sidebar__brand .nf-logo')?.getBoundingClientRect();"
  + "const account=sidebar?.querySelector('.nf-sidebar__account')?.getBoundingClientRect();"
  + "const navLabels=[...sidebar?.querySelectorAll('.nf-nav-item')||[]].filter((el)=>{const box=el.getBoundingClientRect();return box.width>0&&box.height>0}).map((el)=>el.textContent.trim());"
  + "const root=document.documentElement;"
  + "const visible=Boolean(main&&rect&&rect.width>0&&rect.height>0);"
  + "return {"
  + "heading,"
  + "expected:" + JSON.stringify(expectedHeading) + ","
  + "visible,"
  + "viewportWidth:innerWidth,"
  + "scrollWidth:root.scrollWidth,"
  + "overflow:root.scrollWidth>innerWidth+2,"
  + "right:rect?.right||0,"
  + "left:rect?.left||0,"
  + "mainOutside:Boolean(rect&&(rect.right>innerWidth+2||rect.left<-2)),"
  + "mobileHeaderFlex:Boolean(mobileHeader&&getComputedStyle(mobileHeader).display==='flex'),"
  + "mobileHeaderOrdered:Boolean(mobileHeaderRect&&mobileLogo&&mobileBurger&&mobileLogo.left<mobileBurger.left&&mobileBurger.right<=mobileHeaderRect.right+1),"
  + "mobileSidebarCollapsed:Boolean(!sidebarRect||sidebarRect.right<=1||getComputedStyle(sidebar).visibility==='hidden'),"
  + "fullDesktopShell:Boolean(sidebar&&brand&&account&&brand.width>0&&brand.height>0&&account.width>0&&account.height>0&&['Ноды','Трафик','Настройки'].every((label)=>navLabels.includes(label)))"
  + "}})()",
);

const viewports = [
  { name: "desktop", width: 1680, height: 945, mobile: false },
  { name: "desktop-compact", width: 1280, height: 800, mobile: false },
  { name: "tablet", width: 768, height: 1024, mobile: false },
  { name: "mobile", width: 390, height: 844, mobile: true },
];

const pageCases = [];
const screenshots = [];
let executionError = null;

await command("Page.enable");
await command("Runtime.enable");

try {
  await viewport(viewports[0]);
  await command("Page.navigate", { url: appURL("/nodes") });
  await waitFor("document.readyState === 'complete' || document.readyState === 'interactive'", "initial page");

  if (!demoMode) {
    const loginStatus = await evaluate(
      "(async()=>{const response=await fetch('/auth/login',{"
      + "method:'POST',headers:{'Content-Type':'application/json'},"
      + "body:JSON.stringify({token:" + JSON.stringify(panelToken) + "})"
      + "});return response.status})()",
    );
    panelToken = "";
    if (loginStatus !== 200) throw new Error("browser login failed with HTTP " + loginStatus);
    await command("Page.reload", { ignoreCache: true });
  }

  await waitFor(
    "document.querySelector('.nf-nodes-page') && document.querySelector('.nf-node-identity')",
    "nodes overview",
  );
  check(
    "authentication does not expose a token in the page",
    await evaluate("!document.body.innerText.includes('PANEL_TOKEN') && !document.querySelector('input[type=password]')"),
  );

  const nodePath = await evaluate(
    "(()=>{const href=document.querySelector('.nf-node-identity')?.getAttribute('href');"
    + "return href?new URL(href,location.href).pathname:''})()",
  );
  if (!/^\/nodes\/[^/]+$/.test(nodePath)) throw new Error("node detail URL is unavailable");
  const routePath = nodePath + "/routes/new";

  const pagesToCheck = [
    {
      name: "nodes",
      pathname: "/nodes",
      selector: ".nf-nodes-page",
      heading: "Ноды",
      ready: "document.querySelector('.nf-node-row') && document.querySelector('.nf-traffic-module canvas')",
    },
    {
      name: "node-detail",
      pathname: nodePath,
      selector: ".nf-node-detail-page",
      heading: "",
      ready: "document.querySelector('.nf-detail-route-table') && document.querySelector('.nf-detail-kpis')",
    },
    {
      name: "route-editor",
      pathname: routePath,
      selector: ".nf-route-editor-page",
      heading: "Создать маршрут",
      ready: "document.querySelector('.nf-route-editor-layout') && document.querySelector('.nf-code-editor')",
    },
    {
      name: "traffic",
      pathname: "/traffic",
      selector: ".nf-traffic-page",
      heading: "Трафик",
      ready: "document.querySelector('.nf-traffic-page__workspace canvas') && document.querySelector('.nf-traffic-nodes-table tbody tr')",
    },
    {
      name: "settings",
      pathname: "/settings",
      selector: ".nf-settings-page",
      heading: "Настройки",
      ready: "document.querySelector('.nf-settings-grid') && document.querySelector('.nf-release-history')",
    },
  ];

  for (const currentViewport of viewports) {
    await viewport(currentViewport);
    for (const pageCase of pagesToCheck) {
      await navigate(pageCase.pathname, pageCase.ready, pageCase.name + " " + currentViewport.name);
      const inspection = await inspectPage(pageCase.selector, pageCase.heading);
      pageCases.push({
        page: pageCase.name,
        viewport: currentViewport.name,
        width: inspection.viewportWidth,
        scrollWidth: inspection.scrollWidth,
        heading: inspection.heading,
      });
      check(
        pageCase.name + " visible at " + currentViewport.name,
        inspection.visible,
      );
      check(
        pageCase.name + " has no document horizontal overflow at " + currentViewport.name,
        !inspection.overflow,
        inspection.scrollWidth + " > " + inspection.viewportWidth,
      );
      check(
        pageCase.name + " stays inside viewport at " + currentViewport.name,
        !inspection.mainOutside,
        "main bounds " + inspection.left + ".." + inspection.right,
      );
      if (currentViewport.name === "tablet" || currentViewport.name === "mobile") {
        check(
          pageCase.name + " keeps mobile brand and menu separated at " + currentViewport.name,
          inspection.mobileHeaderFlex && inspection.mobileHeaderOrdered,
        );
        check(
          pageCase.name + " keeps the desktop sidebar collapsed at " + currentViewport.name,
          inspection.mobileSidebarCollapsed,
        );
      }
      if ((currentViewport.name === "desktop" || currentViewport.name === "desktop-compact")
          && (pageCase.name === "route-editor" || pageCase.name === "settings")) {
        check(
          pageCase.name + " preserves full sidebar shell at " + currentViewport.name,
          inspection.fullDesktopShell,
        );
      }
      if (pageCase.heading) {
        check(
          pageCase.name + " heading at " + currentViewport.name,
          inspection.heading === pageCase.heading,
          "got " + inspection.heading,
        );
      } else {
        check(
          pageCase.name + " heading at " + currentViewport.name,
          Boolean(inspection.heading),
        );
      }
      if (pageCase.name === "nodes" && currentViewport.name === "desktop-compact") {
        const compactNodes = await evaluate(
          "(()=>{const within=(child,parent)=>{const a=child?.getBoundingClientRect();const b=parent?.getBoundingClientRect();"
          + "return Boolean(a&&b&&a.left>=b.left-1&&a.right<=b.right+1&&a.top>=b.top-1&&a.bottom<=b.bottom+1)};"
          + "const grid=document.querySelector('.nf-overview-grid');const traffic=document.querySelector('.nf-traffic-module');"
          + "const ranking=document.querySelector('.nf-ranking-module');const kpis=document.querySelector('.nf-kpi-stack');"
          + "const routeNames=[...document.querySelectorAll('.nf-ranking__route strong')];"
          + "const speed=[...document.querySelectorAll('.nf-speed-values strong')];"
          + "const health=document.querySelector('.nf-kpi--health');const legend=[...document.querySelectorAll('.nf-health-legend span')];"
          + "const tr=traffic?.getBoundingClientRect();const rr=ranking?.getBoundingClientRect();const kr=kpis?.getBoundingClientRect();"
          + "return {reflow:Boolean(grid&&tr&&rr&&kr&&tr.bottom<=rr.top+2&&Math.abs(rr.top-kr.top)<=2),"
          + "routeNames:Boolean(routeNames.length)&&routeNames.every((el)=>el.scrollWidth<=el.clientWidth+1&&within(el,ranking)),"
          + "speed:Boolean(speed.length)&&speed.every((el)=>el.scrollWidth<=el.clientWidth+1&&within(el,el.closest('.nf-kpi'))),"
          + "health:Boolean(health&&legend.length)&&legend.every((el)=>el.scrollWidth<=el.clientWidth+1&&within(el,health))};})()",
        );
        check("nodes reflows overview below traffic at desktop-compact", compactNodes.reflow);
        check("nodes shows complete top-route names at desktop-compact", compactNodes.routeNames);
        check("nodes keeps RX and TX values inside KPI cards at desktop-compact", compactNodes.speed);
        check("nodes keeps the complete Health legend visible at desktop-compact", compactNodes.health);
      }
      if (pageCase.name === "settings" && currentViewport.name === "desktop-compact") {
        const compactSettings = await evaluate(
          "(()=>{const within=(child,parent)=>{const a=child?.getBoundingClientRect();const b=parent?.getBoundingClientRect();"
          + "return Boolean(a&&b&&a.left>=b.left-1&&a.right<=b.right+1&&a.top>=b.top-1&&a.bottom<=b.bottom+1)};"
          + "const grid=document.querySelector('.nf-settings-grid');const agent=document.querySelector('.nf-agent-settings');"
          + "const first=document.querySelector('.nf-settings-grid > .nf-settings-card');const input=document.querySelector('.nf-agent-scope input');"
          + "const trust=document.querySelector('.nf-agent-trust');const actions=document.querySelector('.nf-agent-release-actions');"
          + "const gr=grid?.getBoundingClientRect();const ar=agent?.getBoundingClientRect();const fr=first?.getBoundingClientRect();"
          + "const style=input&&getComputedStyle(input);const canvas=document.createElement('canvas').getContext('2d');"
          + "if(canvas&&style)canvas.font=style.font;const usable=input&&style?input.clientWidth-parseFloat(style.paddingLeft)-parseFloat(style.paddingRight)-32:0;"
          + "const trustChildren=trust?[...trust.children]:[];const buttons=actions?[...actions.querySelectorAll('button')]:[];"
          + "return {reflow:Boolean(gr&&ar&&fr&&ar.top>=fr.bottom-1&&ar.width>=gr.width-2),"
          + "node:Boolean(input&&input.value.includes('dev-node-01')&&canvas&&canvas.measureText(input.value).width<=usable+1&&within(input,agent)),"
          + "trust:Boolean(trust&&trust.scrollWidth<=trust.clientWidth+1&&trustChildren.every((el)=>within(el,trust))),"
          + "actions:Boolean(actions&&buttons.length&&buttons.every((button)=>within(button,actions)&&button.scrollWidth<=button.clientWidth+1))};})()",
        );
        check("settings moves Agent rollout to a full-width row at desktop-compact", compactSettings.reflow);
        check("settings shows the complete rollout node and IP at desktop-compact", compactSettings.node);
        check("settings keeps release trust state visible at desktop-compact", compactSettings.trust);
        check("settings keeps rollout actions visible at desktop-compact", compactSettings.actions);
      }
      if (pageCase.name === "node-detail" && currentViewport.name === "tablet") {
        const tabletRoutes = await evaluate(
          "(()=>{const within=(child,parent)=>{const a=child?.getBoundingClientRect();const b=parent?.getBoundingClientRect();"
          + "return Boolean(a&&b&&a.left>=b.left-1&&a.right<=b.right+1&&a.top>=b.top-1&&a.bottom<=b.bottom+1)};"
          + "const scroll=document.querySelector('.nf-route-table-scroll');const table=document.querySelector('.nf-detail-route-table');"
          + "const head=document.querySelector('.nf-detail-route-head');const rows=[...document.querySelectorAll('.nf-detail-route-row')];"
          + "const copy=[...document.querySelectorAll('.nf-detail-route-row .nf-route-name strong,.nf-detail-route-row .nf-route-ellipsis,.nf-detail-route-row .nf-route-rate b,.nf-detail-route-row .nf-route-rate small,.nf-detail-route-row .nf-route-quota b,.nf-detail-route-row .nf-route-quota small')];"
          + "const overflow=scroll&&getComputedStyle(scroll).overflowX;return {"
          + "stacked:Boolean(rows.length)&&rows.every((row)=>getComputedStyle(row).display==='grid'&&row.getBoundingClientRect().height>90),"
          + "headHidden:Boolean(head)&&getComputedStyle(head).display==='none',"
          + "noInnerScroll:Boolean(scroll&&table&&overflow!=='auto'&&overflow!=='scroll'&&scroll.scrollWidth<=scroll.clientWidth+2&&table.scrollWidth<=scroll.clientWidth+2&&within(table,scroll)),"
          + "contained:Boolean(rows.length)&&rows.every((row)=>row.scrollWidth<=row.clientWidth+2&&[...row.children].every((child)=>within(child,row))),"
          + "noEllipsis:Boolean(copy.length)&&copy.every((el)=>getComputedStyle(el).textOverflow!=='ellipsis'&&el.scrollWidth<=el.clientWidth+1)};})()",
        );
        check("node detail uses stacked route cards at tablet", tabletRoutes.stacked && tabletRoutes.headHidden);
        check("node detail route cards have no inner horizontal scroll at tablet", tabletRoutes.noInnerScroll);
        check("node detail route-card cells stay inside each card at tablet", tabletRoutes.contained);
        check("node detail route-card values do not ellipsize at tablet", tabletRoutes.noEllipsis);
      }
      if (pageCase.name === "traffic" && currentViewport.name === "mobile") {
        const mobileTraffic = await evaluate(
          "(()=>{const rows=[...document.querySelectorAll('.nf-traffic-routes ol > li:not(.nf-traffic-routes__mobile-more)')];"
          + "const hidden=rows.slice(3).every((row)=>getComputedStyle(row).display==='none');"
          + "const more=document.querySelector('.nf-traffic-routes__mobile-more button');"
          + "const table=document.querySelector('.nf-traffic-nodes-table');"
          + "return {compact:rows.length<=3||hidden,more:rows.length<=3||Boolean(more&&more.getBoundingClientRect().height>=44),"
          + "tableContained:Boolean(table&&table.scrollWidth<=table.clientWidth+2)}})()",
        );
        check("traffic ranking is compact at mobile", mobileTraffic.compact);
        check("traffic ranking exposes a touch-sized mobile expander", mobileTraffic.more);
        check("traffic node rows stay inside their mobile region", mobileTraffic.tableContained);
      }
      screenshots.push(await screenshot(
        pageCase.name + "-" + currentViewport.name + ".png",
      ));
    }
  }

  await viewport(viewports[0]);
  await navigate(
    "/traffic?range=24h",
    "document.querySelector('.nf-traffic-page__workspace canvas') && document.querySelector('.nf-traffic-nodes-table tbody tr')",
    "traffic semantics",
  );
  const trafficSemantics = await evaluate(
    "(()=>{const text=document.querySelector('.nf-traffic-page')?.innerText||'';"
    + "const ranking=document.querySelector('.nf-traffic-routes header p')?.textContent||'';"
    + "const volumes=[...document.querySelectorAll('.nf-traffic-node-volume span')].map((el)=>el.textContent||'');"
    + "const footer=document.querySelector('.nf-traffic-page .nf-chart-footer span');"
    + "return {ranking:ranking.includes('Средняя RX + TX')&&ranking.includes('Все ноды')&&ranking.includes('24 ч'),"
    + "month:volumes.length>0&&volumes.every((value)=>value.includes('квоты')||value.includes('общего объёма')||value.includes('Нет данных за месяц')),"
    + "noRateAsMonth:!text.includes('% текущей скорости'),"
    + "status:Boolean(footer&&/is-(online|degraded|offline|stale)/.test(footer.className)&&footer.textContent.trim())}})()",
  );
  check("traffic ranking states scope and averaging period", trafficSemantics.ranking);
  check("traffic node monthly column uses volume or quota semantics", trafficSemantics.month);
  check("traffic node monthly column does not mix in live-rate share", trafficSemantics.noRateAsMonth);
  check("traffic chart footer exposes a truthful status state", trafficSemantics.status);

  for (const trafficRange of ["1m", "5m", "1h", "24h", "7d", "30d"]) {
    await clickSelector(".nf-range-control input[value='" + trafficRange + "']");
    await waitFor(
      "new URL(location.href).searchParams.get('range')===" + JSON.stringify(trafficRange)
      + " && document.querySelector(\".nf-range-control input[value='" + trafficRange + "']\")?.checked",
      "traffic range " + trafficRange,
    );
    check("traffic supports range " + trafficRange, true);
  }

  const trafficNodeName = await evaluate(
    "document.querySelector('.nf-traffic-nodes-table tbody tr .nf-traffic-node-name strong')?.textContent.trim()||''",
  );
  await clickSelector(".nf-traffic-nodes-table tbody tr button");
  await waitFor(
    "new URL(location.href).searchParams.has('node')"
    + " && document.querySelector('.nf-traffic-page__chart > header p')?.textContent.includes(" + JSON.stringify(trafficNodeName) + ")"
    + " && document.querySelector('.nf-traffic-routes header p')?.textContent.includes(" + JSON.stringify(trafficNodeName) + ")",
    "traffic node scope",
  );
  check("traffic node scope updates chart and ranking together", true);
  await clickSelector(".nf-traffic-nodes-table tbody tr.is-selected button");
  await waitFor(
    "!new URL(location.href).searchParams.has('node')"
    + " && document.querySelector('.nf-traffic-page__chart > header p')?.textContent.includes('Все ноды')",
    "traffic all-node scope",
  );
  check("traffic returns from node scope to all nodes", true);

  await navigate(
    "/traffic?range=24h&node=aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
    "document.querySelector('.nf-traffic-page__recovery') && document.querySelector('.nf-traffic-nodes-table tbody tr')",
    "invalid traffic node recovery",
  );
  const invalidTrafficRecovery = await evaluate(
    "!new URL(location.href).searchParams.has('node')"
    + " && document.querySelector('.nf-traffic-page__recovery')?.textContent.includes('Показан общий трафик')"
    + " && document.querySelector('.nf-traffic-page__chart > header p')?.textContent.includes('Все ноды')",
  );
  check("invalid traffic node recovers visibly to all nodes", invalidTrafficRecovery);

  await viewport(viewports.at(-1));
  await navigate(
    "/nodes",
    "document.querySelector('.nf-node-row') && document.querySelector('.nf-traffic-module canvas')",
    "mobile content priority",
  );
  const mobilePriority = await evaluate(
    "(()=>{const traffic=document.querySelector('.nf-traffic-module')?.getBoundingClientRect();"
    + "const nodes=document.querySelector('.nf-nodes-module')?.getBoundingClientRect();"
    + "const ranking=document.querySelector('.nf-ranking-module')?.getBoundingClientRect();"
    + "const kpis=document.querySelector('.nf-kpi-stack')?.getBoundingClientRect();"
    + "const chart=document.querySelector('.nf-traffic-chart [role=img]')?.getBoundingClientRect();"
    + "const canvas=document.querySelector('.nf-traffic-module canvas')?.getBoundingClientRect();"
    + "return {ordered:Boolean(traffic&&nodes&&ranking&&kpis&&traffic.top<nodes.top&&nodes.top<ranking.top&&ranking.top<kpis.top),"
    + "nodesInFirstViewport:Boolean(nodes&&nodes.top<innerHeight),"
    + "chartFitsAxis:Boolean(chart&&canvas&&canvas.right>=chart.right-2&&canvas.left<=chart.left+2)}})()",
  );
  check("mobile nodes prioritizes traffic then node list", mobilePriority.ordered);
  check("mobile node list starts in the first viewport", mobilePriority.nodesInFirstViewport);
  check("mobile traffic canvas spans its axis container", mobilePriority.chartFitsAxis);

  await command("Emulation.setEmulatedMedia", {
    features: [{ name: "prefers-reduced-motion", value: "reduce" }],
  });
  await viewport(viewports[0]);
  await navigate(
    "/nodes",
    "document.querySelector('.nf-node-row') && document.querySelector('.nf-progress i')",
    "reduced motion",
  );
  const reducedMotion = await evaluate(
    "(()=>{const maxDuration=(value)=>Math.max(...value.split(',').map((item)=>{"
    + "const token=item.trim();const number=parseFloat(token)||0;return token.endsWith('ms')?number/1000:number}));"
    + "const row=getComputedStyle(document.querySelector('.nf-node-row'));"
    + "const progress=getComputedStyle(document.querySelector('.nf-progress i'));"
    + "return maxDuration(row.transitionDuration)<=0.001&&maxDuration(progress.transitionDuration)<=0.001})()",
  );
  check("prefers-reduced-motion suppresses interface transitions", reducedMotion);
  await command("Emulation.setEmulatedMedia", {
    features: [{ name: "prefers-reduced-motion", value: "no-preference" }],
  });

  await viewport(viewports[0]);
  await navigate(
    "/nodes",
    "document.querySelector('.nf-node-identity')",
    "nodes before history test",
  );

  if (demoMode) {
    await evaluate(
      "history.pushState({},'',"
      + JSON.stringify(nodePath + "?demo=1")
      + ");dispatchEvent(new PopStateEvent('popstate'))",
    );
  } else {
    await clickSelector(".nf-node-identity");
  }
  await waitFor(
    "location.pathname===" + JSON.stringify(nodePath) + " && document.querySelector('.nf-node-detail-page')",
    "node detail through navigation",
  );
  check("node navigation has a real URL", true);

  await evaluate("history.back()");
  await waitFor(
    "location.pathname==='/nodes' && document.querySelector('.nf-nodes-page')",
    "browser Back to nodes",
  );
  check("browser Back restores nodes", true);

  await evaluate("history.forward()");
  await waitFor(
    "location.pathname===" + JSON.stringify(nodePath) + " && document.querySelector('.nf-node-detail-page')",
    "browser Forward to node detail",
  );
  check("browser Forward restores node detail", true);

  const nodeDetailDocumentEpoch = await evaluate("performance.timeOrigin");
  await command("Page.reload", { ignoreCache: true });
  await waitFor(
    "performance.timeOrigin!==" + JSON.stringify(nodeDetailDocumentEpoch)
    + " && location.pathname===" + JSON.stringify(nodePath)
    + " && document.querySelector('.nf-detail-kpis')"
    + " && document.querySelector('.nf-detail-route-row')"
    + " && document.querySelector('.nf-detail-versions')",
    "direct node URL reload",
  );
  check("direct node URL survives reload", true);

  const nodeDetail = await evaluate(
    "(()=>{"
    + "const root=document.querySelector('.nf-node-detail-page');"
    + "const text=root?.innerText||'';"
    + "const labels=[...document.querySelectorAll('.nf-detail-kpi__label')].map((el)=>el.textContent.trim());"
    + "const headers=[...document.querySelectorAll('.nf-detail-route-head [role=columnheader]')].map((el)=>el.textContent.trim());"
    + "const hints=[...document.querySelectorAll('.nf-detail-kpi__label [aria-label]')].map((el)=>el.getAttribute('aria-label')||'');"
    + "const audit=document.querySelector('.nf-audit-scroll');"
    + "const auditRows=[...document.querySelectorAll('.nf-audit-row')];"
    + "const auditRowHeight=auditRows[0]?.getBoundingClientRect().height||0;"
    + "const version=document.querySelector('.nf-detail-versions dd')?.textContent.trim()||'';"
    + "return {"
    + "kpis:['CPU','RAM','Load (1м / 5м / 15м)','RX','TX','Соединения','TCP-сессии','Время работы','mTLS соединение'].every((label)=>labels.some((value)=>value.includes(label))),"
    + "routeColumns:['Статус','Маршрут','Слушает','Правило соответствия','Назначение','Соединения','TCP-сессии','RX / TX','Трафик','Бэкенды','Действия'].every((label)=>headers.includes(label)),"
    + "averages:hints.some((value)=>value.includes('среднее')&&value.includes('дельта')),"
    + "processes:hints.some((value)=>value.includes('Процессов:')||value.includes('Основные процессы:'))&&/процесс/.test(text),"
    + "auditScroll:Boolean(audit)&&getComputedStyle(audit).overflowY==='auto'&&(!auditRowHeight||audit.clientHeight<=auditRowHeight*4+4),"
    + "friendlyVersion:/^\\d+\\.\\d+\\.\\d+/.test(version)&&!/-0ubuntu/i.test(version),"
    + "routeToggle:Boolean(document.querySelector('.nf-route-toggle input'))," 
    + "routeActions:Boolean(document.querySelector('.nf-detail-route-row [aria-label*=маршрут],.nf-detail-route-row [aria-label*=Маршрут]'))"
    + "};})()",
  );
  check("node detail exposes the complete 3x3 KPI set", nodeDetail.kpis);
  check("node detail exposes averages and deltas", nodeDetail.averages);
  check("node detail exposes process count and process names", nodeDetail.processes);
  check("node detail exposes all route statistics columns", nodeDetail.routeColumns);
  check("node detail limits audit to four visible rows", nodeDetail.auditScroll);
  check("node detail shows friendly HAProxy version", nodeDetail.friendlyVersion);
  check("node detail exposes immediate route toggle", nodeDetail.routeToggle);
  check("node detail exposes per-route actions", nodeDetail.routeActions);

  await clickSelector("[aria-label='Действия ноды']");
  await clickButton("Переустановить Node Agent");
  await waitFor(
    "document.querySelector('[role=dialog]')?.innerText.includes('Переустановить Node Agent')",
    "Reinstall Node Agent dialog",
  );
  const reinstallDialog = await evaluate(
    "(()=>{const dialog=document.querySelector('[role=dialog]');"
    + "const fields=[...dialog?.querySelectorAll('input')||[]];"
    + "const text=dialog?.innerText||'';return {"
    + "identityLocked:fields.filter((field)=>field.readOnly).length>=2,"
    + "password:text.includes('Пароль SSH'),key:text.includes('Приватный SSH-ключ'),"
    + "sudo:text.includes('Права на сервере'),fingerprint:text.includes('отпечаток SSH-ключа хоста'),"
    + "preserves:text.includes('ID ноды, маршруты и HAProxy-конфигурация сохранятся')"
    + "}})()",
  );
  check("reinstall keeps node identity immutable", reinstallDialog.identityLocked);
  check("reinstall supports password and SSH-key auth", reinstallDialog.password && reinstallDialog.key);
  check("reinstall supports root and sudo access", reinstallDialog.sudo);
  check("reinstall requires a fresh host-key verification", reinstallDialog.fingerprint);
  check("reinstall explains preserved node state", reinstallDialog.preserves);
  screenshots.push(await screenshot("reinstall-node-agent-desktop.png"));
  await pressEscape();
  await waitFor("!document.querySelector('[role=dialog]')", "Reinstall Node Agent dialog close");

  if (!demoMode) {
    const fixtureAt = Date.now();
    const fixture = {
      range: "24h",
      bucket_seconds: 60,
      samples: [
        { timestamp: new Date(fixtureAt - 120_000).toISOString(), rx_bps: null, tx_bps: null },
        { timestamp: new Date(fixtureAt - 60_000).toISOString(), rx_bps: 0, tx_bps: 0 },
        { timestamp: new Date(fixtureAt).toISOString(), rx_bps: null, tx_bps: null },
      ],
    };
    fetchPausedHandler = async ({ requestId, request }) => {
      if (!request.url.includes("/traffic/history")) {
        await command("Fetch.continueRequest", { requestId });
        return;
      }
      const requestedRange = new URL(request.url).searchParams.get("range");
      const body = requestedRange === "24h"
        ? JSON.stringify(fixture)
        : JSON.stringify({ error: { code: "history_unavailable", message: "fixture range failed" } });
      await command("Fetch.fulfillRequest", {
        requestId,
        responseCode: requestedRange === "24h" ? 200 : 503,
        responseHeaders: [{ name: "Content-Type", value: "application/json; charset=utf-8" }],
        body: Buffer.from(body).toString("base64"),
      });
    };
    await command("Fetch.enable", { patterns: [{ urlPattern: "*/traffic/history*", requestStage: "Request" }] });
  }

  await clickSelector(".nf-detail-route-row [aria-label^='Действия маршрута']");
  await clickButton("Статистика");
  await waitFor(
    "document.querySelector('.nf-route-stats-dialog') && (document.querySelector('.nf-route-stats-dialog [role=img]') || document.querySelector('.nf-route-stats-dialog .nf-chart-empty'))",
    "route statistics modal",
  );
  check("route statistics opens from the route action menu", true);

  if (!demoMode) {
    const gapContract = await evaluate(
      "(()=>{const chart=document.querySelector('.nf-route-stats-dialog [role=img]');return {"
      + "rxObserved:Number(chart?.dataset.rxObservedPoints),rxGaps:Number(chart?.dataset.rxGapPoints),"
      + "txObserved:Number(chart?.dataset.txObservedPoints),txGaps:Number(chart?.dataset.txGapPoints),"
      + "copy:chart?.getAttribute('aria-label')||''}})()",
    );
    check(
      "route chart preserves null gaps around an observed zero",
      gapContract.rxObserved === 1 && gapContract.rxGaps === 2
        && gapContract.txObserved === 1 && gapContract.txGaps === 2,
      JSON.stringify(gapContract),
    );
    check(
      "route chart reports the observed zero as zero bitrate",
      gapContract.copy.includes("RX 0 бит/с") && gapContract.copy.includes("TX 0 бит/с"),
      gapContract.copy,
    );

    await clickSelector(".nf-route-stats-dialog .nf-range-control input[value='1h']");
    await waitFor(
      "document.querySelector('.nf-route-stats-dialog .nf-inline-error')?.textContent.includes('fixture range failed')"
      + " && document.querySelector('.nf-route-stats-dialog .nf-chart-empty')"
      + " && !document.querySelector('.nf-route-stats-dialog canvas')",
      "failed route statistics range",
    );
    check("failed route range never keeps the previous series under the new range", true);
    await command("Fetch.disable");
    fetchPausedHandler = null;
    check("route statistics interception has no async errors", asyncEventErrors.length === 0, asyncEventErrors.join("; "));
  } else {
    await clickSelector(".nf-route-stats-dialog .nf-range-control input[value='1h']");
    await waitFor(
      "document.querySelector(\".nf-route-stats-dialog .nf-range-control input[value='1h']\")?.checked",
      "demo route statistics range",
    );
    check("route statistics range switches in demo mode", true);
  }
  await pressEscape();
  await waitFor("!document.querySelector('.nf-route-stats-dialog')", "route statistics modal close");

  await navigate(
    routePath,
    "document.querySelector('.nf-route-editor-layout') && document.querySelector('.nf-code-editor')",
    "route editor behavior",
  );
  const routeEditor = await evaluate(
    "(()=>({"
    + "preview:Boolean(document.querySelector('.nf-code-editor pre code')?.textContent.includes('frontend')),"
    + "draft:Boolean([...document.querySelectorAll('.nf-route-state-field strong')].some((el)=>el.textContent.includes('черновик'))),"
    + "saveDraft:Boolean([...document.querySelectorAll('button')].some((el)=>el.textContent.trim()==='Сохранить черновик')),"
    + "saveEnable:Boolean([...document.querySelectorAll('button')].some((el)=>el.textContent.trim()==='Сохранить и включить')) ,"
    + "valid:document.querySelector('.nf-preview-validation')?.textContent.includes('Конфигурация формы валидна'),"
    + "additional:Boolean(document.querySelector('#route-additional-title')) ,"
    + "safety:Boolean(document.querySelector('.nf-route-safety-note'))"
    + "}))()",
  );
  check("route editor renders HAProxy preview", routeEditor.preview);
  check("new route is a disabled draft", routeEditor.draft);
  check("route editor exposes draft save", routeEditor.saveDraft);
  check("route editor exposes save and enable", routeEditor.saveEnable);
  if (demoMode) check("demo route starts with a valid complete draft", routeEditor.valid);
  check("route editor exposes additional controls", routeEditor.additional);
  check("route editor exposes bottom safety guidance", routeEditor.safety);

  await navigate(
    "/settings",
    "document.querySelector('.nf-settings-grid') && document.querySelector('.nf-release-history')",
    "settings semantics",
  );
  const settings = await evaluate(
    "(()=>{const text=document.querySelector('.nf-settings-page')?.innerText||'';"
    + "return {panel:text.includes('Панель'),security:text.includes('Безопасность'),"
    + "signed:text.includes('подписанные релизы'),history:text.includes('История релизов'),"
    + "rollback:[...document.querySelectorAll('button')].some((el)=>el.textContent.includes('Откат')) ,"
    + "settingsFields:['URL панели','Web/API-порт','Порт канала Agent','Тема панели','Цветовой акцент','Таймаут неактивности','Макс. активных сессий','Ротация учётных данных','Хранить события'].every((label)=>text.includes(label)),"
    + "settingsSave:[...document.querySelectorAll('button')].some((el)=>el.textContent.trim()==='Сохранить')}})()",
  );
  check("settings contains Panel section", settings.panel);
  check("settings contains Security section", settings.security);
  check("settings contains signed Agent releases", settings.signed);
  check("settings contains release history", settings.history);
  check("settings exposes rollback state", settings.rollback);
  if (demoMode) {
    check("demo settings exposes the complete editable contract", settings.settingsFields);
    check("demo settings exposes a real save action", settings.settingsSave);
    const accentBefore = await evaluate("getComputedStyle(document.documentElement).getPropertyValue('--nf-accent').trim().toUpperCase()");
    await clickSelector(".nf-accent-trigger");
    await waitFor("document.querySelector('.nf-accent-popover .mantine-ColorPicker-wrapper')", "custom accent picker");
    const pickerContract = await evaluate(
      "(()=>({custom:Boolean(document.querySelector('.nf-accent-popover .mantine-ColorPicker-wrapper')) ,"
      + "native:Boolean(document.querySelector('.nf-accent-popover input[type=color]'))}))()",
    );
    check("settings uses the in-panel accent picker", pickerContract.custom && !pickerContract.native);
    await clickSelector(".nf-accent-presets button[aria-label='Акцент #EAB308']");
    const accentLive = await evaluate("getComputedStyle(document.documentElement).getPropertyValue('--nf-accent').trim().toUpperCase()");
    check("accent changes live before save", accentLive === "#EAB308", accentLive);
    await pressEscape();
    await clickButton("Сбросить");
    const accentReset = await evaluate("getComputedStyle(document.documentElement).getPropertyValue('--nf-accent').trim().toUpperCase()");
    check("accent reset restores the saved value", accentReset === accentBefore, `${accentReset} != ${accentBefore}`);
  }

  await navigate(
    "/nodes",
    "document.querySelector('.nf-node-identity')",
    "nodes before modal test",
  );
  await clickButton("Добавить ноду");
  await waitFor(
    "document.querySelector('[role=dialog]')?.innerText.includes('Добавить ноду')",
    "Add Node dialog",
  );
  screenshots.push(await screenshot("add-node-modal-desktop.png"));
  const modal = await evaluate(
    "(()=>{const dialog=document.querySelector('[role=dialog]');return {"
    + "title:dialog?.innerText.includes('Добавить ноду'),"
    + "password:dialog?.innerText.includes('Пароль SSH'),"
    + "key:dialog?.innerText.includes('Приватный SSH-ключ'),"
    + "sudo:dialog?.innerText.includes('Права на сервере'),"
    + "ufw:dialog?.innerText.includes('Автоматически открывать listener-порты в UFW'),"
    + "ufwDefaultOff:dialog?.querySelector('input[type=checkbox]')?.checked===false,"
    + "closeLabel:Boolean(dialog?.querySelector('[aria-label=\"Закрыть диалог\"]'))"
    + "}})()",
  );
  check("Add Node dialog supports password auth", modal.password);
  check("Add Node dialog supports SSH-key auth", modal.key);
  check("Add Node dialog exposes sudo mode", modal.sudo);
  check("Add Node dialog explains UFW automation", modal.ufw);
  check("Add Node dialog requires explicit UFW opt-in", modal.ufwDefaultOff);
  check("Add Node dialog close button is accessible", modal.closeLabel);

  const backdropClicked = await evaluate(
    "(()=>{const overlay=document.querySelector('.mantine-Modal-overlay');"
    + "if(!overlay)return false;overlay.dispatchEvent(new MouseEvent('click',{bubbles:true,cancelable:true,view:window}));"
    + "return true})()",
  );
  check("Add Node modal has clickable backdrop", backdropClicked);
  await waitFor(
    "!document.querySelector('[role=dialog]')",
    "modal backdrop close",
  );
  check("clean Add Node modal closes on backdrop", true);

  await clickButton("Добавить ноду");
  await waitFor(
    "document.querySelector('[role=dialog]')?.innerText.includes('Добавить ноду')",
    "Add Node dialog before Escape",
  );
  await pressEscape();
  await waitFor(
    "!document.querySelector('[role=dialog]')",
    "modal Escape close",
  );
  check("clean Add Node modal closes on Escape", true);

  for (const modalViewport of [viewports[2], viewports[3]]) {
    await viewport(modalViewport);
    await navigate(
      "/nodes",
      "document.querySelector('.nf-node-identity')",
      "nodes before Add Node " + modalViewport.name,
    );
    await clickButton("Добавить ноду");
    await waitFor(
      "document.querySelector('[role=dialog]')?.innerText.includes('Добавить ноду')",
      "Add Node dialog " + modalViewport.name,
    );
    const responsiveModal = await evaluate(
      "(()=>{const dialog=document.querySelector('[role=dialog]');const body=dialog?.querySelector('.nf-dialog__body');"
      + "const rect=dialog?.getBoundingClientRect();"
      + "const visible=(el)=>{const r=el.getBoundingClientRect();return r.width>0&&r.height>0};"
      + "const containers=[dialog,body,...(dialog?[...dialog.querySelectorAll('.nf-stepper,.nf-field-grid,.mantine-SegmentedControl-root,.nf-dialog__firewall')]:[])].filter(Boolean);"
      + "const controls=dialog?[...dialog.querySelectorAll('input,button')].filter(visible):[];"
      + "const steps=dialog?[...dialog.querySelectorAll('.nf-stepper span')].map((el)=>el.textContent.replace(/\\s+/g,' ').trim()):[];"
      + 'return {stepOne:Boolean(steps.includes("1 Подключение")&&dialog?.innerText.includes("Название ноды")&&dialog?.innerText.includes("IP-адрес ноды")),'
      + "viewport:Boolean(rect&&rect.left>=-1&&rect.right<=innerWidth+1&&rect.top>=-1&&rect.bottom<=innerHeight+1),"
      + "noOverflow:Boolean(dialog&&document.documentElement.scrollWidth<=innerWidth+2&&containers.every((el)=>el.scrollWidth<=el.clientWidth+2)),"
      + "controls:Boolean(rect&&controls.length&&controls.every((el)=>{const r=el.getBoundingClientRect();return r.left>=rect.left-1&&r.right<=rect.right+1}))};})()",
    );
    check("Add Node shows step 1 at " + modalViewport.name, responsiveModal.stepOne);
    check("Add Node dialog stays inside viewport at " + modalViewport.name, responsiveModal.viewport);
    check("Add Node dialog has no horizontal overflow at " + modalViewport.name, responsiveModal.noOverflow);
    check("Add Node controls stay inside dialog at " + modalViewport.name, responsiveModal.controls);
    screenshots.push(await screenshot("add-node-modal-" + modalViewport.name + ".png"));
    await pressEscape();
    await waitFor(
      "!document.querySelector('[role=dialog]')",
      "Add Node dialog close " + modalViewport.name,
    );
  }
} catch (error) {
  executionError = error instanceof Error ? error.message : String(error);
  failures.push("execution: " + executionError);
} finally {
  panelToken = "";
  socket.close();
}

const report = {
  ok: failures.length === 0,
  mode: demoMode ? "demo" : "authenticated",
  target_origin: origin.origin,
  assertions,
  page_cases: pageCases,
  screenshots,
  failures,
  execution_error: executionError,
};
fs.writeFileSync(
  path.join(outputDirectory, "report.json"),
  JSON.stringify(report, null, 2) + "\n",
  { mode: 0o600 },
);

if (failures.length) {
  console.error("FAIL React live smoke: " + failures.length + " failure(s); report: " + path.join(outputDirectory, "report.json"));
  process.exitCode = 1;
} else {
  console.log("PASS React live smoke: " + assertions.length + " assertions; screenshots: " + screenshots.length);
}
