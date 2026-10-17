import { defineConfig } from 'astro/config';
// 静态站点 + CDN；源站与 App API 不同域（方案 13.4）
export default defineConfig({ output: 'static', site: 'https://yhvpn.io' });
