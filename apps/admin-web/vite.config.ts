import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  server: {
    host: '127.0.0.1',
    port: 15173,
    strictPort: true,
    proxy: { '/admin/v1': { target: 'http://127.0.0.1:18081', changeOrigin: false } },
  },
});
