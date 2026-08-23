import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import { VitePWA } from 'vite-plugin-pwa';
import { appVersion } from './build-version.ts';

/**
 * Hostnames the dev/preview server will answer to, in addition to localhost.
 * Vite rejects unknown Host headers as a DNS-rebinding safeguard; a leading
 * dot allows a domain and all its subdomains. `.ts.net` covers Tailscale
 * MagicDNS names (reach the dev server from another device on your tailnet).
 */
const allowedHosts = ['.ts.net'];

// Where the backend API lives during development. The web dev server proxies
// `/api` here so the browser talks to one origin (no CORS), matching the
// production single-origin deployment.
const API_TARGET = process.env.VITE_API_TARGET ?? 'http://localhost:8788';

export default defineConfig({
  // Stamp the version into the bundle — the browser has no git to ask.
  define: { __APP_VERSION__: JSON.stringify(appVersion()) },
  plugins: [
    react(),
    VitePWA({
      registerType: 'autoUpdate',
      includeAssets: ['icon.svg', 'favicon.svg', 'dev-badge.png', 'dev-badge-full.png'],
      manifest: {
        name: 'Thought Mesh',
        short_name: 'Thought Mesh',
        description: 'Interconnected notes — plain markdown files with wikilinks, backlinks and a graph.',
        theme_color: '#1e1b2e',
        background_color: '#1e1b2e',
        display: 'standalone',
        start_url: '/',
        scope: '/',
        icons: [
          { src: 'icon.svg', sizes: 'any', type: 'image/svg+xml', purpose: 'any maskable' },
        ],
      },
      workbox: {
        // Don't let the service worker cache API calls — notes must be live.
        // The SPA navigation fallback likewise must never answer for the API.
        navigateFallbackDenylist: [/^\/api/],
      },
    }),
  ],
  server: {
    allowedHosts,
    proxy: {
      '/api': { target: API_TARGET, changeOrigin: true },
    },
  },
  preview: {
    allowedHosts,
    proxy: {
      '/api': { target: API_TARGET, changeOrigin: true },
    },
  },
});
