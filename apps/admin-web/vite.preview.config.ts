import {defineConfig} from 'vite';
import react from '@vitejs/plugin-react';
// Independent build. Never use this output as an authenticated admin deployment.
export default defineConfig({plugins:[react()],build:{outDir:'preview-dist',rollupOptions:{input:'preview.html'}},preview:{host:'127.0.0.1',port:15175,strictPort:true}});
