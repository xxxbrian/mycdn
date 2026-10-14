import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/api': process.env.MYCDN_ADMIN_URL || 'http://127.0.0.1:8081',
      '/healthz': process.env.MYCDN_ADMIN_URL || 'http://127.0.0.1:8081',
    },
  },
  build: {
    target: 'es2022',
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (id.includes('/node_modules/') && id.includes('/zrender/')) return 'chart-renderer';
          if (id.includes('/node_modules/') && id.includes('/echarts/')) return 'charts';
        },
      },
    },
  },
});
