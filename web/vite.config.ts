import { defineConfig } from 'vite';

// Dev: `npm run dev:web` with a hub running (`baton mock` or `baton up --dev`); /api is proxied to it.
export default defineConfig({
  root: import.meta.dirname,
  base: './',
  // three.js is one lazy chunk (~600 kB, loaded only for the scene, served locally): no warning for it.
  build: { outDir: 'dist', emptyOutDir: true, target: 'es2022', chunkSizeWarningLimit: 800 },
  server: {
    proxy: { '/api': { target: process.env.BATON_HUB_URL ?? 'http://127.0.0.1:7777', changeOrigin: false } },
  },
});
