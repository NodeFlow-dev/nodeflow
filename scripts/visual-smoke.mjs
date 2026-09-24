import fs from "node:fs";
import path from "node:path";

const [, , targetURL, outputDirectory] = process.argv;
const token = process.env.PANEL_TOKEN;
if (!targetURL || !outputDirectory || !token) throw new Error("usage: PANEL_TOKEN=... node visual-smoke.mjs panel-url output-directory");

const baseURL = new URL(targetURL);
const appURL = pathname => new URL(pathname, baseURL).href;
const delay = milliseconds => new Promise(resolve => setTimeout(resolve, milliseconds));
fs.mkdirSync(outputDirectory, { recursive: true });

let pages;
for (let attempt = 0; attempt < 40; attempt++) {
  try {
    pages = await fetch("http://127.0.0.1:9229/json/list").then(response => response.json());
    if (pages.length) break;
  } catch {}
  await delay(100);
}
if (!pages?.length) throw new Error("Chromium DevTools endpoint is unavailable");

const page = pages.find(item => item.type === "page") || pages[0];
const socket = new WebSocket(page.webSocketDebuggerUrl);
await new Promise((resolve, reject) => {
  socket.addEventListener("open", resolve, { once: true });
  socket.addEventListener("error", reject, { once: true });
});

let sequence = 0;
const pending = new Map();
socket.addEventListener("message", event => {
  const message = JSON.parse(event.data);
  if (!message.id || !pending.has(message.id)) return;
  const { resolve, reject } = pending.get(message.id);
  pending.delete(message.id);
  if (message.error) reject(new Error(message.error.message));
  else resolve(message.result);
});
const command = (method, params = {}) => new Promise((resolve, reject) => {
  const id = ++sequence;
  pending.set(id, { resolve, reject });
  socket.send(JSON.stringify({ id, method, params }));
});
const evaluate = async expression => {
  const result = await command("Runtime.evaluate", { expression, awaitPromise: true, returnByValue: true });
  if (result.exceptionDetails) {
    const detail = result.exceptionDetails.exception?.description || result.exceptionDetails.text || "page evaluation failed";
    throw new Error(detail);
  }
  return result.result.value;
};
const screenshot = async name => {
  const result = await command("Page.captureScreenshot", { format: "png", captureBeyondViewport: true, fromSurface: true });
  fs.writeFileSync(path.join(outputDirectory, name), Buffer.from(result.data, "base64"));
};
const viewport = (width, height, mobile = false) => command("Emulation.setDeviceMetricsOverride", {
  width,
  height,
  deviceScaleFactor: 1,
  mobile,
  screenWidth: width,
  screenHeight: height
});
const click = async selector => {
  const clicked = await evaluate(`(()=>{const element=document.querySelector(${JSON.stringify(selector)});if(!element)return false;element.click();return true})()`);
  if (!clicked) throw new Error(`element not found: ${selector}`);
  await delay(300);
};
const waitFor = async (expression, label, timeout = 10000) => {
  const started = Date.now();
  let lastError;
  while (Date.now() - started < timeout) {
    try {
      if (await evaluate(`Boolean(${expression})`)) return;
    } catch (error) {
      lastError = error;
    }
    await delay(100);
  }
  throw new Error(`timeout waiting for ${label}${lastError ? `: ${lastError.message}` : ""}`);
};
const pressEscape = async () => {
  await command("Input.dispatchKeyEvent", { type: "keyDown", key: "Escape", code: "Escape", windowsVirtualKeyCode: 27, nativeVirtualKeyCode: 27 });
  await command("Input.dispatchKeyEvent", { type: "keyUp", key: "Escape", code: "Escape", windowsVirtualKeyCode: 27, nativeVirtualKeyCode: 27 });
  await delay(250);
};
const forceCloseDialogs = () => evaluate(`(()=>{for(const dialog of document.querySelectorAll('dialog[open]')){if(dialog.dataset.busy==='true'){if(dialog.id==='nodeDialog')setNodeFormBusy(false);if(dialog.id==='credentialDialog')setCredentialBusy(false)}dialog.close()}return true})()`);

await command("Page.enable");
await command("Runtime.enable");

const results = {};
const failures = [];
const uniqueSNI = `visual-smoke-${Date.now()}.invalid`;
const listenerPort = 62000 + (Date.now() % 3000);
let nodeID = "";
let createdRouteID = "";
let executionError = null;

try {
  await viewport(1440, 1000);
  await command("Page.navigate", { url: appURL("/nodes") });
  await delay(1000);
  const login = await evaluate(`(async()=>{const response=await fetch('/auth/login',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({token:${JSON.stringify(token)}})});return {status:response.status,body:await response.text()}})()`);
  if (login.status !== 200) throw new Error(`browser login failed: ${login.status} ${login.body}`);
  await command("Page.reload", { ignoreCache: true });
  await waitFor(`!document.querySelector('#shell')?.hidden&&document.querySelectorAll('.node-row').length>0`, "nodes after login");
  await waitFor(`!document.querySelector('#trafficChart .chart-loading')&&!document.querySelector('#topRoutes .ranking-loading')`, "dashboard after login");
  await evaluate(`window.__visualSmokeNativeConfirm=window.confirm;window.__visualSmokeNativeConfirmCalls=0;window.confirm=()=>{window.__visualSmokeNativeConfirmCalls++;return false}`);

  results.nodesDesktop = await evaluate(`(()=>{const visible=element=>Boolean(element&&element.getClientRects().length),nav=[...document.querySelectorAll('.sidebar .nav-item')].map(item=>item.textContent.trim().replace(/^[▦⚙]\\s*/u,'')),row=document.querySelector('.node-row'),head=document.querySelector('.node-table-head'),rangeButtons=[...document.querySelectorAll('#dashboardRange [data-range]')],statusButtons=[...document.querySelectorAll('#nodeStatusTabs [data-status]')],dashboard=document.querySelector('.nodes-dashboard'),traffic=document.querySelector('.traffic-dashboard-panel'),ranking=document.querySelector('.routes-ranking-panel'),kpis=document.querySelector('.dashboard-kpis');return {pathname:location.pathname,title:document.querySelector('#pageTitle')?.textContent,nodeRows:document.querySelectorAll('.node-row').length,nav,routeSidebar:Boolean(document.querySelector('.sidebar [data-view="routes"]')),allRoutes:Boolean(document.querySelector('#openAllRoutes')),topSearch:visible(document.querySelector('#topNodeSearch')),topFilter:visible(document.querySelector('#topNodeFilter')),topSettings:visible(document.querySelector('#topSettingsButton')),manualRefresh:visible(document.querySelector('#refreshButton')),dashboard:visible(dashboard),trafficChart:visible(document.querySelector('#trafficChart')),chartContent:Boolean(document.querySelector('#trafficChart .chart-canvas,#trafficChart .chart-empty')),topRoutes:visible(document.querySelector('#topRoutes')),rankingContent:Boolean(document.querySelector('#topRoutes .ranking-rows,#topRoutes .ranking-empty')),kpis:visible(kpis),kpiCount:document.querySelectorAll('.dashboard-kpis .dashboard-panel').length,statusTabs:statusButtons.length,rangePressed:rangeButtons.filter(button=>button.getAttribute('aria-pressed')==='true').map(button=>button.dataset.range),statusPressed:statusButtons.filter(button=>button.getAttribute('aria-pressed')==='true').map(button=>button.dataset.status),headerColumns:head?getComputedStyle(head).gridTemplateColumns.trim().split(/\\s+/).length:0,headerCells:head?.children.length||0,rowColumns:row?getComputedStyle(row).gridTemplateColumns.trim().split(/\\s+/).length:0,rowCells:row?.children.length||0,visibleRowCells:row?[...row.children].filter(visible).length:0,dashboardWidth:dashboard?.getBoundingClientRect().width||0,trafficWidth:traffic?.getBoundingClientRect().width||0,rankingWidth:ranking?.getBoundingClientRect().width||0,kpiWidth:kpis?.getBoundingClientRect().width||0,overflow:document.documentElement.scrollWidth>innerWidth}})()`);
  results.nodeListScope = await evaluate(`(()=>{const list=document.querySelector('#nodeList'),surface=document.querySelector('#nodesView .nodes-table-surface'),listStyle=getComputedStyle(list),surfaceStyle=getComputedStyle(surface);return {maxHeight:listStyle.maxHeight,overflowY:listStyle.overflowY,surfaceOverflow:surfaceStyle.overflow,nodeRows:list.querySelectorAll('.node-row').length}})()`);
  results.topFilter = await evaluate(`(()=>{const details=document.querySelector('#topNodeFilter');details.open=true;const buttons=[...details.querySelectorAll('[data-top-status]')];const result={open:details.open,count:buttons.length,pressed:buttons.filter(button=>button.getAttribute('aria-pressed')==='true').map(button=>button.dataset.topStatus)};details.open=false;return result})()`);
  results.manualRefresh = await evaluate(`(()=>{const button=document.querySelector('#refreshButton'),before={type:button.type,label:button.getAttribute('aria-label'),disabled:button.disabled,loading:button.classList.contains('is-loading')};button.click();return {before,during:{disabled:button.disabled,loading:button.classList.contains('is-loading')}}})()`);
  await waitFor(`!document.querySelector('#refreshButton').disabled&&!document.querySelector('#refreshButton').classList.contains('is-loading')`, "manual dashboard refresh", 15000);
  results.manualRefresh.settled = await evaluate(`!document.querySelector('#refreshButton').disabled&&!document.querySelector('#refreshButton').classList.contains('is-loading')`);
  nodeID = await evaluate(`state.nodes[0]?.id||''`);
  if (!nodeID) throw new Error("visual smoke requires at least one node");
  await screenshot("nodes-desktop.png");

  // Clean dialogs light-dismiss, dirty dialogs ask, and busy dialogs stay protected.
  await click("[data-open-node]");
  await evaluate(`document.querySelector('#nodeDialog').dispatchEvent(new MouseEvent('click',{bubbles:true}))`);
  await waitFor(`!document.querySelector('#nodeDialog').open`, "clean dialog backdrop close");
  results.dialogBackdrop = await evaluate(`({closed:!document.querySelector('#nodeDialog').open})`);
  await click("[data-open-node]");
  await evaluate(`document.querySelector('#nodeDialog').focus()`);
  await pressEscape();
  await waitFor(`!document.querySelector('#nodeDialog').open`, "clean dialog Escape close");
  results.dialogEscape = await evaluate(`({closed:!document.querySelector('#nodeDialog').open})`);
  await click("[data-open-node]");
  await evaluate(`(()=>{const dialog=document.querySelector('#nodeDialog'),input=document.querySelector('#nodeForm [name="name"]');input.value='Несохранённое имя';input.dispatchEvent(new Event('input',{bubbles:true}));dialog.dispatchEvent(new MouseEvent('click',{bubbles:true}))})()`);
  await waitFor(`document.querySelector('#confirmDialog').open`, "custom dirty-dialog confirmation");
  results.dialogDirtyPrompt = await evaluate(`({open:document.querySelector('#confirmDialog').open,title:document.querySelector('#confirmDialogTitle').textContent,message:document.querySelector('#confirmDialogMessage').textContent,accept:document.querySelector('#confirmDialogAccept').textContent,nodeOpen:document.querySelector('#nodeDialog').open})`);
  await click("#confirmDialogCancel");
  await waitFor(`!document.querySelector('#confirmDialog').open&&document.querySelector('#nodeDialog').open`, "dirty dialog cancel");
  await evaluate(`document.querySelector('#nodeDialog').dispatchEvent(new MouseEvent('click',{bubbles:true}))`);
  await waitFor(`document.querySelector('#confirmDialog').open`, "dirty dialog second confirmation");
  await click("#confirmDialogAccept");
  await waitFor(`!document.querySelector('#confirmDialog').open&&!document.querySelector('#nodeDialog').open`, "dirty dialog confirmed close");
  results.dialogDirtyGuard = { guarded: results.dialogDirtyPrompt.nodeOpen, closed: true };
  await click("[data-open-node]");
  await evaluate(`setNodeFormBusy(true,'bootstrap','Проверяем защиту занятого окна')`);
  results.dialogBusyGuard = await evaluate(`(()=>{const dialog=document.querySelector('#nodeDialog');dialog.dispatchEvent(new MouseEvent('click',{bubbles:true}));return {open:dialog.open,progress:!document.querySelector('#nodeInstallProgress').hidden,closeDisabled:document.querySelector('#nodeDialog [data-close-dialog]').disabled,agentPort:document.querySelector('#nodeForm [name="agent_port"]').value}})()`);
  await screenshot("add-node-busy-desktop.png");
  await forceCloseDialogs();

  await click("#accentPicker summary");
  const originalAccent = await evaluate(`({h:document.querySelector('#hue').value,s:document.querySelector('#sat').value,l:document.querySelector('#light').value})`);
  results.pickerDesktop = await evaluate(`(()=>{for(const [id,value] of [['hue',240],['sat',70],['light',55]]){const input=document.querySelector('#'+id);input.value=value;input.dispatchEvent(new Event('input',{bubbles:true}))}return {open:document.querySelector('#accentPicker').open,contrast:Number(document.documentElement.dataset.accentContrast),surfaceContrast:Number(document.documentElement.dataset.accentSurfaceContrast),label:document.querySelector('#accentPicker summary').getAttribute('aria-label')}})()`);
  await screenshot("picker-desktop.png");
  await evaluate(`(()=>{const values=${JSON.stringify(originalAccent)};for(const [id,key] of [['hue','h'],['sat','s'],['light','l']]){const input=document.querySelector('#'+id);input.value=values[key];input.dispatchEvent(new Event('input',{bubbles:true}))}document.querySelector('#accentPicker').open=false})()`);

  // A node is a real URL and browser Back/Forward restores both list and detail.
  await click(`.node-row[data-id=${JSON.stringify(nodeID)}]`);
  await waitFor(`location.pathname===${JSON.stringify(`/nodes/${nodeID}`)}&&document.querySelector('#nodeDetailView')?.classList.contains('active')`, "node detail route");
  results.historyEntered = await evaluate(`({pathname:location.pathname,title:document.querySelector('.detail-title h2')?.textContent})`);
  await evaluate(`history.back()`);
  await waitFor(`location.pathname==='/nodes'&&document.querySelector('#nodesView')?.classList.contains('active')`, "browser back to nodes");
  results.historyBack = await evaluate(`({pathname:location.pathname,title:document.querySelector('#pageTitle')?.textContent})`);
  await evaluate(`history.forward()`);
  await waitFor(`location.pathname===${JSON.stringify(`/nodes/${nodeID}`)}&&document.querySelector('#nodeDetailView')?.classList.contains('active')`, "browser forward to node");
  results.historyForward = await evaluate(`({pathname:location.pathname,title:document.querySelector('.detail-title h2')?.textContent})`);
  await command("Page.reload", { ignoreCache: true });
  await waitFor(`location.pathname===${JSON.stringify(`/nodes/${nodeID}`)}&&document.querySelector('#nodeDetailView')?.classList.contains('active')&&document.querySelector('.routes-panel')`, "direct node URL reload");
  await evaluate(`window.__visualSmokeNativeConfirm=window.confirm;window.__visualSmokeNativeConfirmCalls=0;window.confirm=()=>{window.__visualSmokeNativeConfirmCalls++;return false}`);
  results.directNodeURL = await evaluate(`({pathname:location.pathname,title:document.querySelector('.detail-title h2')?.textContent})`);

  results.nodeDesktop = await evaluate(`(()=>{const visible=element=>Boolean(element&&element.getClientRects().length),buttons=[...document.querySelectorAll('#nodeDetail button')].filter(visible).map(button=>button.textContent.trim()),audit=document.querySelector('.audit-list'),auditRows=[...document.querySelectorAll('.audit-row')],rowHeight=auditRows[0]?.getBoundingClientRect().height||0,rollback=document.querySelector('[data-agent-rollback]');return {createRoute:Boolean(document.querySelector('[data-detail-add-route]')),manualRefresh:Boolean(document.querySelector('#refreshButton')),revisionControls:visible(document.querySelector('[data-render-config]'))||visible(document.querySelector('[data-assign-revision]'))||visible(document.querySelector('[data-preview-revision]')),revisionText:document.querySelector('#nodeDetail')?.innerText.includes('История ревизий'),manualApply:buttons.some(text=>/^(Применить|Обновить)$/.test(text)||text.includes('Собрать конфиг')),rollbackVisible:visible(rollback),rollbackEnabled:Boolean(rollback&&!rollback.disabled),auditRows:auditRows.length,auditHeight:audit?.clientHeight||0,auditScrollHeight:audit?.scrollHeight||0,auditRowHeight:rowHeight,auditOverflow:getComputedStyle(audit).overflowY,overflow:document.documentElement.scrollWidth>innerWidth}})()`);
  await screenshot("node-desktop.png");

  const activeRouteID = await evaluate(`state.nodeDetail?.routes?.value?.find(route=>route.enabled)?.id||''`);
  if (activeRouteID) {
    await click(`[data-route-edit=${JSON.stringify(activeRouteID)}]`);
    await waitFor(`location.pathname===${JSON.stringify(`/nodes/${nodeID}/routes/${activeRouteID}/edit`)}&&document.querySelector('#routeEditorView')?.classList.contains('active')`, "active route edit page");
    results.activeEdit = await evaluate(`({available:true,behavior:document.querySelector('#routeSaveBehavior')?.textContent,footer:document.querySelector('#routeFooterNote')?.textContent,save:document.querySelector('#saveRouteButton')?.textContent})`);
    await evaluate(`history.back()`);
    await waitFor(`location.pathname===${JSON.stringify(`/nodes/${nodeID}`)}&&document.querySelector('#nodeDetailView')?.classList.contains('active')&&document.querySelector('[data-detail-add-route]')`, "back from active route editor");
  } else {
    results.activeEdit = { available: false };
  }

  // Creating a route is a full page. The first save must create a disabled draft only.
  await click("[data-detail-add-route]");
  await waitFor(`location.pathname===${JSON.stringify(`/nodes/${nodeID}/routes/new`)}&&document.querySelector('#routeEditorView')?.classList.contains('active')`, "new route page");
  results.routeNewPage = await evaluate(`({pathname:location.pathname,active:document.querySelector('#routeEditorView').classList.contains('active'),dialog:Boolean(document.querySelector('#routeDialog')),state:document.querySelector('#routeEditorState')?.textContent,behavior:document.querySelector('#routeSaveBehavior')?.textContent,node:document.querySelector('#routeForm [name="node_id"]')?.value,save:document.querySelector('#saveRouteButton')?.textContent,modes:[...document.querySelectorAll('.route-mode-choice label')].map(label=>label.textContent.trim()),selected:document.querySelector('#routeForm [name="route_mode"]:checked')?.value})`);
  await evaluate(`(()=>{const form=document.querySelector('#routeForm'),set=(name,value)=>{const control=form.elements[name];control.value=value;control.dispatchEvent(new Event('input',{bubbles:true}));control.dispatchEvent(new Event('change',{bubbles:true}))};set('listener_ip','*');set('listener_port',${listenerPort});set('snis',${JSON.stringify(uniqueSNI)});set('target_host','127.0.0.1');set('target_port','9');return true})()`);
  results.compactQuota = await evaluate(`(()=>{const form=document.querySelector('#routeForm'),toggle=form.elements.quota_enabled,period=form.elements.quota_period,proxy=form.elements.proxy_protocol,proxyField=proxy.closest('.field'),quota=form.querySelector('.quota-control'),copy=document.querySelector('[data-quota-toggle-copy]'),settings=document.querySelector('[data-quota-settings]'),periodOptions=[...period.options].map(option=>[option.value,option.textContent.trim()]);const initial={copy:copy.textContent,hidden:settings.hidden,role:toggle.getAttribute('role'),period:period.value,periodDisabled:period.disabled,periodOptions};toggle.checked=true;toggle.dispatchEvent(new Event('change',{bubbles:true}));const enabled={copy:copy.textContent,hidden:settings.hidden,periodDisabled:period.disabled,proxyHeight:proxy.getBoundingClientRect().height,proxyFieldHeight:proxyField.getBoundingClientRect().height,quotaHeight:quota.getBoundingClientRect().height};toggle.checked=false;toggle.dispatchEvent(new Event('change',{bubbles:true}));return {initial,enabled,reset:{copy:copy.textContent,hidden:settings.hidden}}})()`);
  results.manualBackendValidation = await evaluate(`(()=>{const field=document.querySelector('#routeForm [name="custom_fragment"]'),zone=document.querySelector('#customFragmentZone');zone.open=true;field.value='backend injected';field.dispatchEvent(new Event('input',{bubbles:true}));state.routeValidationSubmitted=true;updateRouteBuilder();const invalid={aria:field.getAttribute('aria-invalid'),summary:document.querySelector('#routeFormError').textContent};field.value=${JSON.stringify("timeout connect 5s\noption tcp-check")};state.routeValidationSubmitted=false;document.querySelector('#routeFormError').textContent='';field.dispatchEvent(new Event('input',{bubbles:true}));return {invalid,preview:document.querySelector('#haproxyPreview').textContent,zoneOpen:zone.open,bytes:document.querySelector('#fragmentBytes').textContent}})()`);
  results.routeValidation = await evaluate(`(()=>{const form=document.querySelector('#routeForm'),port=form.elements.listener_port;port.value='0';port.dispatchEvent(new Event('input',{bubbles:true}));state.routeValidationSubmitted=true;updateRouteBuilder();const invalid={invalid:port.getAttribute('aria-invalid'),describedBy:port.getAttribute('aria-describedby'),summary:document.querySelector('#routeFormError').textContent};port.value=${JSON.stringify(String(listenerPort))};state.routeValidationSubmitted=false;document.querySelector('#routeFormError').textContent='';port.dispatchEvent(new Event('input',{bubbles:true}));return invalid})()`);
  results.routeEditorDesktop = await evaluate(`({preview:document.querySelector('#haproxyPreview').textContent.includes('frontend'),fullPage:document.querySelector('#routeEditorView').classList.contains('active'),overflow:document.documentElement.scrollWidth>innerWidth,columns:getComputedStyle(document.querySelector('.route-editor')).gridTemplateColumns})`);
  await screenshot("route-new-desktop.png");

  await viewport(768, 900);
  await delay(300);
  results.routeEditorTablet = await evaluate(`({width:innerWidth,overflow:document.documentElement.scrollWidth>innerWidth,columns:getComputedStyle(document.querySelector('.route-editor')).gridTemplateColumns,formWidth:document.querySelector('#routeForm').getBoundingClientRect().width})`);
  await screenshot("route-new-tablet.png");

  await viewport(390, 844, true);
  await delay(300);
  results.routeEditorMobile = await evaluate(`({width:innerWidth,overflow:document.documentElement.scrollWidth>innerWidth,preview:document.querySelector('#haproxyPreview').textContent.includes('frontend'),formWidth:document.querySelector('#routeForm').getBoundingClientRect().width})`);
  await screenshot("route-new-mobile.png");

  await viewport(1440, 1000);
  await delay(250);
  await evaluate(`document.querySelector('#routeForm').requestSubmit()`);
  await waitFor(`location.pathname===${JSON.stringify(`/nodes/${nodeID}`)}&&document.querySelector('#nodeDetailView')?.classList.contains('active')`, "draft save return to node", 15000);
  await waitFor(`Boolean(state.nodeDetail?.routes?.value?.find(route=>routeSNIs(route).includes(${JSON.stringify(uniqueSNI)})))`, "created draft in route list", 10000);
  const createdRoute = await evaluate(`(()=>{const route=state.nodeDetail.routes.value.find(item=>routeSNIs(item).includes(${JSON.stringify(uniqueSNI)})),toggle=document.querySelector('[data-route-toggle="'+route.id+'"]'),row=toggle?.closest('.detail-route');return {id:route.id,enabled:Boolean(route.enabled),deployed:Boolean(route.deployed),deploymentState:route.deployment_state,deletePending:Boolean(route.delete_pending),toggleChecked:Boolean(toggle?.checked),toggleTitle:toggle?.closest('label')?.title||'',edit:Boolean(row?.querySelector('[data-route-edit]')),remove:Boolean(row?.querySelector('[data-route-delete]')),status:row?.querySelector('.deployment-state')?.textContent.trim()||'',manual:String(route.custom_fragment||'')}})()`);
  createdRouteID = createdRoute.id;
  results.createdDraft = createdRoute;
  await screenshot("route-draft-node-desktop.png");

  // A disabled draft can be edited at its own URL without applying it.
  await click(`[data-route-edit=${JSON.stringify(createdRouteID)}]`);
  await waitFor(`location.pathname===${JSON.stringify(`/nodes/${nodeID}/routes/${createdRouteID}/edit`)}&&document.querySelector('#routeEditorView')?.classList.contains('active')`, "draft edit page");
  results.editDraft = await evaluate(`({pathname:location.pathname,state:document.querySelector('#routeEditorState')?.textContent,behavior:document.querySelector('#routeSaveBehavior')?.textContent,sni:document.querySelector('#routeForm [name="snis"]')?.value,manual:document.querySelector('#routeForm [name="custom_fragment"]')?.value,save:document.querySelector('#saveRouteButton')?.textContent})`);
  await screenshot("route-edit-draft-desktop.png");
  await evaluate(`history.back()`);
  await waitFor(`location.pathname===${JSON.stringify(`/nodes/${nodeID}`)}&&document.querySelector('#nodeDetailView')?.classList.contains('active')`, "back from route editor");

  // All Routes is an auxiliary node-list action, not a sidebar tab.
  await click("#backToNodes");
  await waitFor(`location.pathname==='/nodes'&&document.querySelector('#nodesView')?.classList.contains('active')`, "nodes before all routes");
  await click("#openAllRoutes");
  await waitFor(`location.pathname==='/routes'&&document.querySelector('#routesView')?.classList.contains('active')`, "all routes page");
  await waitFor(`Boolean(document.querySelector('[data-route-toggle=${JSON.stringify(createdRouteID)}]'))`, "draft row in all routes");
  results.allRoutesDesktop = await evaluate(`(()=>{const nav=[...document.querySelectorAll('.sidebar .nav-item')].map(item=>item.textContent.trim().replace(/^[▦⚙]\\s*/u,'')),toggle=document.querySelector('[data-route-toggle=${JSON.stringify(createdRouteID)}]'),row=toggle?.closest('tr');return {pathname:location.pathname,title:document.querySelector('#pageTitle')?.textContent,nav,sidebarRoute:Boolean(document.querySelector('.sidebar [data-view="routes"]')),filter:Boolean(document.querySelector('#routeNodeFilter')),search:Boolean(document.querySelector('#routeSearch')),sort:Boolean(document.querySelector('#routeSort')),draftToggle:!toggle?.checked,edit:Boolean(row?.querySelector('[data-route-edit]')),remove:Boolean(row?.querySelector('[data-route-delete]')),overflow:document.documentElement.scrollWidth>innerWidth,tableScrollable:document.querySelector('#routesView .table-wrap').scrollWidth>=document.querySelector('#routesView .table-wrap').clientWidth}})()`);
  await screenshot("all-routes-desktop.png");
  await click("#backFromAllRoutes");
  await waitFor(`location.pathname==='/nodes'&&document.querySelector('#nodesView')?.classList.contains('active')`, "all routes back to nodes");

  // Tablet and mobile geometry for list, detail and full-page editor.
  await viewport(768, 900);
  await delay(300);
  results.nodesTablet = await evaluate(`(()=>{const visible=element=>Boolean(element&&element.getClientRects().length),row=document.querySelector('.node-row'),surface=document.querySelector('#nodeList'),traffic=document.querySelector('.traffic-dashboard-panel'),ranking=document.querySelector('.routes-ranking-panel'),kpis=document.querySelector('.dashboard-kpis'),toolbar=document.querySelector('.nodes-table-toolbar'),dashboard=document.querySelector('.nodes-dashboard'),rect=element=>element?.getBoundingClientRect()||{top:0,right:0,width:0};return {overflow:document.documentElement.scrollWidth>innerWidth,nodeRows:document.querySelectorAll('.node-row').length,rowClipped:Boolean(row)&&(row.scrollWidth>row.clientWidth||row.getBoundingClientRect().right>surface.getBoundingClientRect().right+1),visibleDesktopCells:row?[...row.querySelectorAll('.node-cell')].filter(visible).length:-1,compactVisible:visible(row?.querySelector('.node-compact-stats')),headerHidden:!visible(document.querySelector('.node-table-head')),dashboardVisible:visible(dashboard),trafficVisible:visible(traffic)&&visible(document.querySelector('#trafficChart')),rankingVisible:visible(ranking)&&visible(document.querySelector('#topRoutes')),kpisVisible:visible(kpis)&&document.querySelectorAll('.dashboard-kpis .dashboard-panel').length>=4,verticalOrder:rect(traffic).top<rect(ranking).top&&rect(ranking).top<rect(kpis).top&&rect(kpis).top<rect(toolbar).top,dashboardWithinViewport:rect(dashboard).right<=innerWidth+1}})()`);
  await screenshot("nodes-tablet.png");
  await click(`.node-row[data-id=${JSON.stringify(nodeID)}]`);
  await waitFor(`document.querySelector('#nodeDetailView')?.classList.contains('active')&&document.querySelector('[data-detail-add-route]')`, "tablet node detail");
  results.nodeTablet = await evaluate(`(()=>{const routes=state.nodeDetail?.routes?.value||[],activeRoute=routes.find(route=>route.enabled&&route.deployed),rows=[...document.querySelectorAll('.routes-panel .detail-route')],row=rows.find(candidate=>candidate.querySelector('[data-route-toggle]')?.dataset.routeToggle===activeRoute?.id)||rows.find(candidate=>candidate.querySelector('[data-route-toggle]:checked'))||rows[0],target=row?.querySelector('.route-target');return {overflow:document.documentElement.scrollWidth>innerWidth,targetVisible:Boolean(target)&&getComputedStyle(target).display!=='none',targetWidth:target?.getBoundingClientRect().width||0,rowClipped:Boolean(row)&&row.scrollWidth>row.clientWidth,backendHealthVisible:Boolean(target?.querySelector('.status'))}})()`);
  await screenshot("node-tablet.png");
  await click("[data-detail-add-route]");
  await waitFor(`document.querySelector('#routeEditorView')?.classList.contains('active')`, "tablet route editor");
  results.routePageTablet = await evaluate(`({overflow:document.documentElement.scrollWidth>innerWidth,columns:getComputedStyle(document.querySelector('.route-editor')).gridTemplateColumns})`);
  await screenshot("route-page-tablet.png");
  await click("#backFromRoute");
  await waitFor(`document.querySelector('#nodeDetailView')?.classList.contains('active')&&document.querySelector('[data-detail-add-route]')`, "tablet back from editor");
  await click("#backToNodes");
  await waitFor(`document.querySelector('#nodesView')?.classList.contains('active')`, "tablet nodes");
  await waitFor(`!document.querySelector('#trafficChart .chart-loading')&&!document.querySelector('#topRoutes .ranking-loading')`, "tablet dashboard return");

  await viewport(390, 844, true);
  await delay(300);
  results.nodesMobile = await evaluate(`(()=>{const visible=element=>Boolean(element&&element.getClientRects().length),traffic=document.querySelector('.traffic-dashboard-panel'),ranking=document.querySelector('.routes-ranking-panel'),kpis=document.querySelector('.dashboard-kpis'),toolbar=document.querySelector('.nodes-table-toolbar'),dashboard=document.querySelector('.nodes-dashboard'),refresh=document.querySelector('#refreshButton'),rect=element=>element?.getBoundingClientRect()||{top:0,right:0,width:0};return {width:innerWidth,scrollWidth:document.documentElement.scrollWidth,overflow:document.documentElement.scrollWidth>innerWidth,nodeRows:document.querySelectorAll('.node-row').length,compactStats:document.querySelectorAll('.node-compact-stats>span').length,compactVisible:visible(document.querySelector('.node-compact-stats')),manualRefresh:visible(refresh),refreshTouchTarget:Math.min(refresh?.getBoundingClientRect().width||0,refresh?.getBoundingClientRect().height||0),dashboardVisible:visible(dashboard),trafficVisible:visible(traffic)&&visible(document.querySelector('#trafficChart')),rankingVisible:visible(ranking)&&visible(document.querySelector('#topRoutes')),kpisVisible:visible(kpis)&&document.querySelectorAll('.dashboard-kpis .dashboard-panel').length>=4,verticalOrder:rect(traffic).top<rect(ranking).top&&rect(ranking).top<rect(kpis).top&&rect(kpis).top<rect(toolbar).top,dashboardWithinViewport:rect(dashboard).right<=innerWidth+1,statusTabsVisible:visible(document.querySelector('#nodeStatusTabs')),tableSearchVisible:visible(document.querySelector('#nodeSearch'))}})()`);
  await screenshot("nodes-mobile.png");
  await click("#menuButton");
  results.mobileMenuOpen = await evaluate(`({expanded:document.querySelector('#menuButton').getAttribute('aria-expanded'),backdrop:!document.querySelector('#sidebarBackdrop').hidden,nav:[...document.querySelectorAll('.sidebar .nav-item')].map(item=>item.textContent.trim().replace(/^[▦⚙]\\s*/u,''))})`);
  await screenshot("mobile-menu.png");
  await pressEscape();
  results.mobileMenuClosed = await evaluate(`({expanded:document.querySelector('#menuButton').getAttribute('aria-expanded'),backdrop:!document.querySelector('#sidebarBackdrop').hidden})`);
  await click(`.node-row[data-id=${JSON.stringify(nodeID)}]`);
  await waitFor(`document.querySelector('#nodeDetailView')?.classList.contains('active')&&document.querySelector('[data-detail-add-route]')`, "mobile node detail");
  results.nodeMobile = await evaluate(`(()=>{const target=document.querySelector('.routes-panel .route-target');return {title:document.querySelector('.detail-title h2')?.textContent,overflow:document.documentElement.scrollWidth>innerWidth,auditVisible:Boolean(document.querySelector('.audit-panel')),rates:[...document.querySelectorAll('.stat-box span')].some(span=>span.textContent==='Подключений/с'),routeTargetVisible:Boolean(target)&&getComputedStyle(target).display!=='none',routeControls:Boolean(document.querySelector('[data-route-toggle]')&&document.querySelector('[data-route-edit]')&&document.querySelector('[data-route-delete]'))}})()`);
  await screenshot("node-mobile.png");
  await click("[data-detail-add-route]");
  await waitFor(`document.querySelector('#routeEditorView')?.classList.contains('active')`, "mobile route editor");
  results.routePageMobile = await evaluate(`({overflow:document.documentElement.scrollWidth>innerWidth,fullPage:document.querySelector('#routeEditorView').classList.contains('active'),preview:document.querySelector('#haproxyPreview').textContent.includes('frontend'),width:document.querySelector('#routeForm').getBoundingClientRect().width})`);
  await screenshot("route-page-mobile.png");
  await click("#backFromRoute");
  await waitFor(`document.querySelector('#nodeDetailView')?.classList.contains('active')&&document.querySelector('[data-detail-add-route]')`, "mobile back from editor");

  // Delete only the draft created by this smoke. It has never been enabled or applied.
  await viewport(1440, 1000);
  await delay(250);
  await click(`[data-route-delete=${JSON.stringify(createdRouteID)}]`);
  await waitFor(`document.querySelector('#confirmDialog').open`, "custom route delete confirmation");
  results.deletePrompt = await evaluate(`({open:document.querySelector('#confirmDialog').open,title:document.querySelector('#confirmDialogTitle').textContent,message:document.querySelector('#confirmDialogMessage').textContent,detail:document.querySelector('#confirmDialogDetail').textContent,accept:document.querySelector('#confirmDialogAccept').textContent,danger:document.querySelector('#confirmDialog').dataset.tone})`);
  await screenshot("route-delete-confirm-desktop.png");
  await click("#confirmDialogAccept");
  await waitFor(`!state.nodeDetail?.routes?.value?.some(route=>route.id===${JSON.stringify(createdRouteID)})`, "draft deletion", 10000);
  results.deleteDraft = await evaluate(`({removed:!document.querySelector('#nodeDetail [data-route-delete=${JSON.stringify(createdRouteID)}]'),pathname:location.pathname})`);
  createdRouteID = "";

  results.confirmImplementation = await evaluate(`({nativeCalls:window.__visualSmokeNativeConfirmCalls,customDialog:Boolean(document.querySelector('#confirmDialog'))})`);
  await evaluate(`window.confirm=window.__visualSmokeNativeConfirm;delete window.__visualSmokeNativeConfirm;delete window.__visualSmokeNativeConfirmCalls`);

  await click("[data-view=\"settings\"]");
  await waitFor(`location.pathname==='/settings'&&document.querySelector('#settingsView')?.classList.contains('active')`, "settings route");
  results.settingsDesktop = await evaluate(`({pathname:location.pathname,title:document.querySelector('#pageTitle')?.textContent,signing:document.querySelector('#releaseSigningKey')?.textContent,overflow:document.documentElement.scrollWidth>innerWidth})`);
  await screenshot("settings-desktop.png");
} catch (error) {
  executionError = error;
} finally {
  // Best-effort cleanup if a later assertion/navigation failed after draft creation.
  if (nodeID) {
    try {
      await evaluate(`(async()=>{const response=await fetch('/api/v1/nodes/${encodeURIComponent(nodeID)}/routes');if(!response.ok)return {status:response.status};const routes=await response.json();const matches=routes.filter(route=>routeSNIs(route).includes(${JSON.stringify(uniqueSNI)}));const statuses=[];for(const route of matches){const deleted=await fetch('/api/v1/nodes/${encodeURIComponent(nodeID)}/routes/'+encodeURIComponent(route.id),{method:'DELETE'});statuses.push(deleted.status)}return {matches:matches.length,statuses}})()`);
    } catch {}
  }
  try { await evaluate(`if(window.__visualSmokeNativeConfirm){window.confirm=window.__visualSmokeNativeConfirm;delete window.__visualSmokeNativeConfirm;delete window.__visualSmokeNativeConfirmCalls}`); } catch {}
  try { await forceCloseDialogs(); } catch {}
  socket.close();
}

const expect = (condition, label) => { if (!condition) failures.push(label); };
for (const [section, value] of Object.entries(results)) {
  if (!value || typeof value !== "object") continue;
  for (const [key, state] of Object.entries(value)) {
    if ((key === "overflow" || key.endsWith("Overflow")) && state === true) failures.push(`${section}.${key}`);
  }
}

expect(results.nodesDesktop?.pathname === "/nodes" && results.nodesDesktop?.title === "Ноды" && results.nodesDesktop?.nodeRows > 0, "nodes desktop data and URL");
expect(JSON.stringify(results.nodesDesktop?.nav) === JSON.stringify(["Ноды", "Настройки"]) && !results.nodesDesktop?.routeSidebar && results.nodesDesktop?.allRoutes, "sidebar only nodes/settings with auxiliary all-routes action");
expect(results.nodesDesktop?.topSearch && results.nodesDesktop?.topFilter && results.nodesDesktop?.topSettings && results.nodesDesktop?.manualRefresh, "desktop top search filter settings and refresh controls");
expect(results.topFilter?.open && results.topFilter?.count === 4 && JSON.stringify(results.topFilter?.pressed) === JSON.stringify(["all"]), "top filter semantic pressed state");
expect(results.manualRefresh?.before?.type === "button" && results.manualRefresh?.before?.label && !results.manualRefresh?.before?.disabled && results.manualRefresh?.during?.disabled && results.manualRefresh?.during?.loading && results.manualRefresh?.settled, "manual refresh loading lifecycle");
expect(results.nodesDesktop?.dashboard && results.nodesDesktop?.trafficChart && results.nodesDesktop?.chartContent && results.nodesDesktop?.topRoutes && results.nodesDesktop?.rankingContent && results.nodesDesktop?.kpis && results.nodesDesktop?.kpiCount >= 4, "desktop dashboard chart ranking and KPI content");
expect(JSON.stringify(results.nodesDesktop?.rangePressed) === JSON.stringify(["24h"]) && JSON.stringify(results.nodesDesktop?.statusPressed) === JSON.stringify(["all"]) && results.nodesDesktop?.statusTabs === 4, "dashboard range and node-status semantic pressed states");
expect(results.nodesDesktop?.headerColumns === 9 && results.nodesDesktop?.headerCells === 9 && results.nodesDesktop?.rowColumns === 9 && results.nodesDesktop?.rowCells === 9 && results.nodesDesktop?.visibleRowCells === 9, "desktop node table has nine aligned visible columns");
expect(results.nodesDesktop?.dashboardWidth > 0 && results.nodesDesktop?.trafficWidth > results.nodesDesktop?.rankingWidth && results.nodesDesktop?.trafficWidth > results.nodesDesktop?.kpiWidth, "desktop dashboard composition proportions");
expect(results.nodeListScope?.maxHeight === "none" && results.nodeListScope?.overflowY === "visible" && results.nodeListScope?.surfaceOverflow === "visible", "node list has no four-row height cap");
expect(results.dialogBackdrop?.closed && results.dialogEscape?.closed, "clean dialogs close on backdrop and Escape");
expect(results.dialogDirtyGuard?.guarded && results.dialogDirtyGuard?.closed, "dirty dialog confirmation guard");
expect(results.dialogDirtyPrompt?.open && /Закрыть без сохранения/i.test(results.dialogDirtyPrompt?.title || "") && /Закрыть без сохранения/i.test(results.dialogDirtyPrompt?.accept || ""), "custom dirty-dialog copy");
expect(results.dialogBusyGuard?.open && results.dialogBusyGuard?.progress && results.dialogBusyGuard?.closeDisabled && results.dialogBusyGuard?.agentPort === "4200", "busy dialog protection");
expect(results.pickerDesktop?.open && results.pickerDesktop?.contrast >= 4.5 && results.pickerDesktop?.surfaceContrast >= 4.5, "accent AA contrast");
expect(results.historyEntered?.pathname === `/nodes/${nodeID}` && results.historyBack?.pathname === "/nodes" && results.historyForward?.pathname === `/nodes/${nodeID}`, "browser back/forward navigation");
expect(results.directNodeURL?.pathname === `/nodes/${nodeID}` && results.directNodeURL?.title, "direct node URL reload");
expect(results.nodeDesktop?.createRoute && results.nodeDesktop?.manualRefresh && !results.nodeDesktop?.revisionControls && !results.nodeDesktop?.revisionText && !results.nodeDesktop?.manualApply, "node page without manual revision/apply flow");
expect(results.nodeDesktop?.rollbackVisible && results.nodeDesktop?.rollbackEnabled, "node page exposes safe signed Agent rollback");
expect(results.nodeDesktop?.auditRows > 4 && results.nodeDesktop?.auditOverflow === "auto" && results.nodeDesktop?.auditHeight <= results.nodeDesktop?.auditRowHeight * 4 + 1 && results.nodeDesktop?.auditScrollHeight > results.nodeDesktop?.auditHeight, "node audit shows four rows then scrolls internally");
expect(!results.activeEdit?.available || /примен/i.test(results.activeEdit?.behavior || "") && results.activeEdit?.save === "Сохранить и применить", "active route edit explains automatic apply");
expect(results.routeNewPage?.pathname === `/nodes/${nodeID}/routes/new` && results.routeNewPage?.active && !results.routeNewPage?.dialog && /выключенн/i.test(results.routeNewPage?.behavior || "") && results.routeNewPage?.save === "Сохранить черновик", "new route full-page disabled-draft copy");
expect(results.routeNewPage?.selected === "sni" && results.routeNewPage?.modes?.some(value=>value.includes("По домену (SNI)")) && results.routeNewPage?.modes?.some(value=>value.includes("Весь трафик этого IP/порта")), "route mode plain-language copy");
expect(results.compactQuota?.initial?.copy === "Без лимита" && results.compactQuota?.initial?.hidden && results.compactQuota?.initial?.role === "switch" && results.compactQuota?.enabled?.copy === "Лимит включён" && !results.compactQuota?.enabled?.hidden && results.compactQuota?.reset?.hidden, "compact quota switch");
expect(results.compactQuota?.initial?.period === "calendar_month" && results.compactQuota?.initial?.periodDisabled && !results.compactQuota?.enabled?.periodDisabled && JSON.stringify(results.compactQuota?.initial?.periodOptions) === JSON.stringify([["hourly","Каждый час"],["daily","Каждые сутки"],["calendar_month","Календарный месяц"],["monthly_from_creation","Месяц от даты создания"]]), "quota period selector contract");
expect(results.compactQuota?.enabled?.proxyHeight > 0 && results.compactQuota?.enabled?.proxyHeight <= 48 && results.compactQuota?.enabled?.proxyFieldHeight < results.compactQuota?.enabled?.quotaHeight, "PROXY protocol field stays compact");
expect(results.manualBackendValidation?.invalid?.aria === "true" && results.manualBackendValidation?.invalid?.summary && results.manualBackendValidation?.zoneOpen && results.manualBackendValidation?.preview.includes("timeout connect 5s") && results.manualBackendValidation?.preview.includes("option tcp-check"), "manual backend validation and live preview");
expect(results.routeValidation?.invalid === "true" && results.routeValidation?.describedBy && results.routeValidation?.summary, "route validation accessibility");
expect(results.routeEditorDesktop?.fullPage && results.routeEditorDesktop?.preview, "route editor desktop preview");
expect((results.routeEditorTablet?.columns || "").split(" ").length === 1 && results.routeEditorTablet?.formWidth > 0, "route editor tablet single column");
expect(results.routeEditorMobile?.preview && results.routeEditorMobile?.formWidth > 0, "route editor mobile preview");
expect(results.createdDraft?.enabled === false && results.createdDraft?.deployed === false && results.createdDraft?.deploymentState === "draft" && !results.createdDraft?.deletePending && results.createdDraft?.manual.includes("timeout connect 5s"), "new route persisted as disabled undeployed draft");
expect(!results.createdDraft?.toggleChecked && /включить и применить/i.test(results.createdDraft?.toggleTitle || "") && results.createdDraft?.edit && results.createdDraft?.remove, "draft toggle/edit/delete controls");
expect(results.editDraft?.pathname === `/nodes/${nodeID}/routes/${results.createdDraft?.id}/edit` && /останется выключенным/i.test(results.editDraft?.behavior || "") && results.editDraft?.sni === uniqueSNI && results.editDraft?.manual.includes("option tcp-check") && results.editDraft?.save === "Сохранить черновик", "disabled draft edit flow");
expect(results.allRoutesDesktop?.pathname === "/routes" && results.allRoutesDesktop?.title === "Все маршруты" && !results.allRoutesDesktop?.sidebarRoute && results.allRoutesDesktop?.filter && results.allRoutesDesktop?.search && results.allRoutesDesktop?.sort, "auxiliary all-routes search page");
expect(results.allRoutesDesktop?.draftToggle && results.allRoutesDesktop?.edit && results.allRoutesDesktop?.remove, "all-routes draft controls");
expect(results.nodesTablet?.nodeRows > 0 && !results.nodesTablet?.rowClipped && results.nodesTablet?.visibleDesktopCells === 0 && results.nodesTablet?.compactVisible && results.nodesTablet?.headerHidden, "tablet node row geometry");
expect(results.nodesTablet?.dashboardVisible && results.nodesTablet?.trafficVisible && results.nodesTablet?.rankingVisible && results.nodesTablet?.kpisVisible && results.nodesTablet?.verticalOrder && results.nodesTablet?.dashboardWithinViewport, "tablet dashboard sections visible and reordered");
expect(results.nodeTablet?.targetVisible && results.nodeTablet?.targetWidth > 180 && !results.nodeTablet?.rowClipped && results.nodeTablet?.backendHealthVisible, "tablet route target geometry");
expect((results.routePageTablet?.columns || "").split(" ").length === 1, "tablet route page layout");
expect(results.nodesMobile?.nodeRows > 0 && results.nodesMobile?.compactStats >= 4 && results.nodesMobile?.compactVisible && results.nodesMobile?.manualRefresh && results.nodesMobile?.refreshTouchTarget >= 44, "mobile node list and refresh target");
expect(results.nodesMobile?.dashboardVisible && results.nodesMobile?.trafficVisible && results.nodesMobile?.rankingVisible && results.nodesMobile?.kpisVisible && results.nodesMobile?.verticalOrder && results.nodesMobile?.dashboardWithinViewport && results.nodesMobile?.statusTabsVisible && results.nodesMobile?.tableSearchVisible, "mobile dashboard essentials visible and reordered");
expect(results.mobileMenuOpen?.expanded === "true" && results.mobileMenuOpen?.backdrop && JSON.stringify(results.mobileMenuOpen?.nav) === JSON.stringify(["Ноды", "Настройки"]), "mobile menu open");
expect(results.mobileMenuClosed?.expanded === "false" && !results.mobileMenuClosed?.backdrop, "mobile menu Escape close");
expect(results.nodeMobile?.title && results.nodeMobile?.auditVisible && results.nodeMobile?.rates && results.nodeMobile?.routeTargetVisible && results.nodeMobile?.routeControls, "mobile node detail and route controls");
expect(results.routePageMobile?.fullPage && results.routePageMobile?.preview && results.routePageMobile?.width > 0, "mobile full-page route editor");
expect(results.deletePrompt?.open && /Удалить маршрут/i.test(results.deletePrompt?.title || "") && results.deletePrompt?.accept === "Удалить маршрут" && results.deletePrompt?.danger === "danger", "custom destructive route confirmation");
expect(results.deleteDraft?.removed && results.deleteDraft?.pathname === `/nodes/${nodeID}`, "disabled draft delete");
expect(results.confirmImplementation?.customDialog && results.confirmImplementation?.nativeCalls === 0, "no native confirm dialogs");
expect(results.settingsDesktop?.pathname === "/settings" && results.settingsDesktop?.title === "Настройки" && results.settingsDesktop?.signing && results.settingsDesktop?.signing !== "Загрузка…", "settings direct route");

const payload = { ok: !executionError && failures.length === 0, failures, results };
process.stdout.write(JSON.stringify(payload));
if (executionError) throw executionError;
if (failures.length) throw new Error(`visual smoke failed: ${failures.join(", ")}`);
