import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
  plugins: [sveltekit()],
  server: {
    proxy: {
      '/_oberwatch': {
        target: 'http://localhost:8080',
        changeOrigin: true,
      },
    },
  },
});
