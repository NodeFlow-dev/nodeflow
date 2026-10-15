import { Navigate, Route, Routes, useLocation } from 'react-router-dom';
import { lazy, Suspense } from 'react';
import { AppFrame } from '../components/AppFrame';
import { demoSuffix } from '../lib/navigation';
import { t } from '../i18n';

const NodesOverviewPage = lazy(() => import('../pages/NodesOverviewPage').then((module) => ({ default: module.NodesOverviewPage })));
const NodeDetailPage = lazy(() => import('../pages/NodeDetailPage').then((module) => ({ default: module.NodeDetailPage })));
const RouteEditorPage = lazy(() => import('../pages/RouteEditorPage').then((module) => ({ default: module.RouteEditorPage })));
const SettingsPage = lazy(() => import('../pages/SettingsPage').then((module) => ({ default: module.SettingsPage })));
const HAProxyConfigPage = lazy(() => import('../pages/HAProxyConfigPage').then((module) => ({ default: module.HAProxyConfigPage })));
const TrafficPage = lazy(() => import('../pages/TrafficPage').then((module) => ({ default: module.TrafficPage })));

export function App() {
  const location = useLocation();
  const nodesURL = `/nodes${demoSuffix(location.search)}`;
  return (
    <Routes>
      <Route element={<AppFrame />}>
        <Route index element={<Navigate to={nodesURL} replace />} />
        <Route path="/nodes" element={<Suspense fallback={<div className="nf-route-loader" aria-label={t('app.loadingInterface')} />}><NodesOverviewPage /></Suspense>} />
        <Route path="/nodes/:nodeId" element={<Suspense fallback={<div className="nf-route-loader" aria-label={t('app.loadingNode')} />}><NodeDetailPage /></Suspense>} />
        <Route path="/nodes/:nodeId/haproxy" element={<Suspense fallback={<div className="nf-route-loader" aria-label={t('app.loadingConfigurationEditor')} />}><HAProxyConfigPage /></Suspense>} />
        <Route path="/nodes/:nodeId/routes/new" element={<Suspense fallback={<div className="nf-route-loader" aria-label={t('app.loadingRouteEditor')} />}><RouteEditorPage /></Suspense>} />
        <Route path="/nodes/:nodeId/routes/:routeId/edit" element={<Suspense fallback={<div className="nf-route-loader" aria-label={t('app.loadingRouteEditor')} />}><RouteEditorPage /></Suspense>} />
        <Route path="/nodes/:nodeId/routes/:routeId" element={<Suspense fallback={<div className="nf-route-loader" aria-label={t('app.loadingRouteEditor')} />}><RouteEditorPage /></Suspense>} />
        <Route path="/traffic" element={<Suspense fallback={<div className="nf-route-loader" aria-label={t('app.loadingTrafficStatistics')} />}><TrafficPage /></Suspense>} />
        <Route path="/settings" element={<Suspense fallback={<div className="nf-route-loader" aria-label={t('app.loadingSettings')} />}><SettingsPage /></Suspense>} />
        <Route path="*" element={<Navigate to={nodesURL} replace />} />
      </Route>
    </Routes>
  );
}
