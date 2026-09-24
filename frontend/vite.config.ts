import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

function frontendChunk(id: string) {
  const normalizedId = id.replaceAll('\\', '/');

  // ECharts is already imported through its tree-shakeable core API. Keep its
  // renderer in a separate cacheable chunk so the shared TrafficChart does not
  // turn into a single >500 kB asset on every route that renders a graph.
  if (normalizedId.includes('/node_modules/zrender/')) return 'chart-renderer';
  if (normalizedId.includes('/node_modules/echarts-for-react/')) return 'chart-react';
  if (normalizedId.includes('/node_modules/echarts/')) return 'chart-engine';

  return undefined;
}

export default defineConfig(({ mode }) => ({
  plugins: [react()],
  build: {
    outDir: 'dist',
    // Production assets are embedded in the Panel binary.  Shipping source
    // maps would expose source and materially inflate every Panel image.
    sourcemap: mode !== 'production',
    emptyOutDir: true,
    rollupOptions: {
      output: {
        manualChunks: frontendChunk,
      },
    },
  },
  server: {
    host: '0.0.0.0',
    port: 5173,
    proxy: {
      '/api': 'http://127.0.0.1:8080',
      '/auth': 'http://127.0.0.1:8080',
      '/healthz': 'http://127.0.0.1:8080',
    },
  },
}));
