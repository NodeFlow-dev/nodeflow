export function demoSuffix(search: string): string {
  return new URLSearchParams(search).get('demo') === '1' ? '?demo=1' : '';
}

type RoutePreloader = {
  matches: (pathname: string) => boolean;
  load: () => Promise<unknown>;
};

const routePreloaders: RoutePreloader[] = [
  { matches: (pathname) => pathname === '/nodes', load: () => import('../pages/NodesOverviewPage') },
  { matches: (pathname) => /^\/nodes\/[^/]+\/routes\/(?:new|[^/]+(?:\/edit)?)$/.test(pathname), load: () => import('../pages/RouteEditorPage') },
  { matches: (pathname) => /^\/nodes\/[^/]+$/.test(pathname), load: () => import('../pages/NodeDetailPage') },
  { matches: (pathname) => pathname === '/traffic', load: () => import('../pages/TrafficPage') },
  { matches: (pathname) => pathname === '/settings', load: () => import('../pages/SettingsPage') },
];

const routePreloads = new Map<RoutePreloader, Promise<unknown>>();

export function preloadAppRoute(pathname: string): void {
  const preloader = routePreloaders.find((candidate) => candidate.matches(pathname));
  if (!preloader || routePreloads.has(preloader)) return;
  const request = preloader.load().catch((error) => {
    routePreloads.delete(preloader);
    throw error;
  });
  routePreloads.set(preloader, request);
  void request.catch(() => undefined);
}

export function preloadInternalLink(target: EventTarget | null): void {
  if (!(target instanceof Element)) return;
  const anchor = target.closest<HTMLAnchorElement>('a[href]');
  if (!anchor) return;
  const url = new URL(anchor.href, window.location.href);
  if (url.origin !== window.location.origin) return;
  preloadAppRoute(url.pathname);
}
