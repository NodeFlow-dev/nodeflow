import { api } from '../../lib/api';
import type { NodeRecord, RouteRecord } from '../../lib/contracts';
import { randomUUID } from '../../lib/uuid';
import { routePayload, routeToDraft } from './model';

import { templateDraft, type RouteTemplate } from './templateModel';
export { templateDraft, type RouteTemplate } from './templateModel';

export interface CopyResult { node_id: string; route?: RouteRecord; error?: string }
const key = 'nodeflow:demo-route-templates';
const demo = () => new URLSearchParams(window.location.search).get('demo') === '1';
const demoTemplates = (): RouteTemplate[] => JSON.parse(localStorage.getItem(key) || '[]');
export async function listTemplates(): Promise<RouteTemplate[]> {
  return demo() ? demoTemplates() : api('/api/v1/route-templates');
}
export async function getTemplate(id: string): Promise<RouteTemplate> {
  if (!demo()) return api(`/api/v1/route-templates/${encodeURIComponent(id)}`);
  const value = demoTemplates().find(t => t.id === id);
  if (!value) throw new Error('Шаблон не найден');
  return value;
}
export async function saveTemplate(name: string, description: string, config: ReturnType<typeof routePayload>, previous?: RouteTemplate): Promise<RouteTemplate> {
  if (!demo()) return api(`/api/v1/route-templates${previous ? `/${previous.id}` : ''}`, {
    method: previous ? 'PUT' : 'POST', body: JSON.stringify({ name, description, config, ...(previous ? { expected_version: previous.version } : {}) }),
  });
  const list = demoTemplates();
  const value = { id: previous?.id || randomUUID(), name, description, config, version: (previous?.version || 0) + 1 };
  localStorage.setItem(key, JSON.stringify([...list.filter(t => t.id !== value.id), value]));
  return value;
}
export async function saveRouteTemplate(source: RouteRecord, name: string, description: string): Promise<RouteTemplate> {
  if (demo()) return saveTemplate(name, description, routePayload(routeToDraft(source), false));
  return api('/api/v1/route-templates', { method: 'POST', body: JSON.stringify({ name, description, source_node_id: source.node_id, source_route_id: source.id }) });
}
export async function deleteTemplate(template: RouteTemplate): Promise<void> {
  if (!demo()) return api(`/api/v1/route-templates/${template.id}`, { method: 'DELETE', body: JSON.stringify({ expected_version: template.version }) });
  localStorage.setItem(key, JSON.stringify(demoTemplates().filter(t => t.id !== template.id)));
}
export async function templateNodes(): Promise<NodeRecord[]> {
  if (!demo()) return api('/api/v1/nodes');
  const { demoNodeBundles } = await import('../../fixtures/demo');
  return demoNodeBundles.map(b => b.node);
}
export async function copyRoutes(source: RouteRecord | RouteTemplate, nodes: string[], name: string): Promise<CopyResult[]> {
  const isTemplate = 'config' in source;
  if (!demo()) return api(isTemplate ? `/api/v1/route-templates/${source.id}/apply` : `/api/v1/nodes/${source.node_id}/routes/${source.id}/copy`, {
    method: 'POST', body: JSON.stringify({ node_ids: nodes, name, ...(isTemplate ? { expected_version: source.version } : {}) }),
  });
  const { upsertDemoRoute } = await import('../../fixtures/demo');
  return nodes.map(node_id => {
    const draft = isTemplate ? templateDraft(source) : routeToDraft(source);
    const payload = routePayload({ ...draft, name: name || (node_id === (isTemplate ? '' : source.node_id) ? `${draft.name} (копия)` : draft.name) }, false);
    const route = { ...payload, id: randomUUID(), node_id, version: 1, enabled: false, deployment_state: 'draft', snis: payload.snis } as unknown as RouteRecord;
    upsertDemoRoute(node_id, route);
    return { node_id, route };
  });
}
