import { defineConfig } from 'vite';

// Dev: `npm run dev:web` with a hub running (`baton mock` or `baton up --dev`); /api is proxied to it.
export default defineConfig({
  root: import.meta.dirname,
  base: './',
  build: { outDir: 'dist', emptyOutDir: true, target: 'es2022' },
  server: {
    proxy: { '/api': { target: process.env.BATON_HUB_URL ?? 'http://127.0.0.1:7777', changeOrigin: false } },
  },
});
