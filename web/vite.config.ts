import { defineConfig } from 'vite';

// Dev: `npm run dev:web` with a hub running (`baton mock` or `baton up --dev`); /api is proxied to it.
export default defineConfig({
  root: import.meta.dirname,
  base: './',
  // three.js is one lazy chunk (~600 kB, loaded only for the scene, served locally): no warning for it.
  build: { outDir: 'dist', emptyOutDir: true, target: 'es2022', chunkSizeWarningLimit: 800 },
  server: {
    // changeOrigin: the hub only accepts a loopback Host header (DNS-rebinding guard)
    proxy: { '/api': { target: process.env.BATON_HUB_URL ?? 'http://127.0.0.1:7777', changeOrigin: true } },
    // never serve local notes or secrets, even when the dev server is exposed with --host
    fs: { deny: ['.env', '.env.*', '*.{crt,pem}', '**/.git/**', '**/*.local.md', '**/*.local.*'] },
  },
});
