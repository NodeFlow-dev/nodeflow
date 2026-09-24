import '@mantine/core/styles.css';
import './styles/global.css';
import { MantineProvider } from '@mantine/core';
import { focusManager, QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { createBrowserRouter, RouterProvider } from 'react-router-dom';
import { App } from './app/App';
import { nodeFlowTheme } from './theme/theme';
import { initialiseNodeFlowAppearance } from './lib/appearance';
import { agentReleasesQueryKey } from './lib/polling';

const demoAppearance = new URLSearchParams(window.location.search).get('demo') === '1'
  || import.meta.env.VITE_NODEFLOW_DEMO === 'true';
initialiseNodeFlowAppearance(demoAppearance);

const queryClient = new QueryClient({
  defaultOptions: {
    queries: { refetchOnWindowFocus: false, gcTime: 5 * 60_000 },
  },
});
// Agent releases are cached with staleTime: Infinity (never polled). Mark them
// stale when the tab regains visibility so the focus refetch of live queries
// picks up releases uploaded elsewhere. Subscribed before QueryClientProvider
// mounts, so this runs ahead of the query cache's own focus handler.
focusManager.subscribe((focused) => {
  if (focused) void queryClient.invalidateQueries({ queryKey: agentReleasesQueryKey, refetchType: 'none' });
});
const router = createBrowserRouter([{ path: '*', element: <App /> }]);

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <MantineProvider theme={nodeFlowTheme} defaultColorScheme="dark">
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </MantineProvider>
  </StrictMode>,
);
