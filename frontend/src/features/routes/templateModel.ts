import type { RouteRecord } from '../../lib/contracts';
import { routeToDraft, type RouteDraft } from './model';

export interface RouteTemplate {
  id: string;
  name: string;
  description: string;
  config: Record<string, unknown>;
  version: number;
}

export function templateDraft(template: RouteTemplate): RouteDraft {
  return routeToDraft({ ...template.config, id: '', node_id: '', version: 1, enabled: false } as unknown as RouteRecord);
}
