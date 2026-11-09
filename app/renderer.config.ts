// The renderer's Vite config, shared by the electron-vite build
// (electron.vite.config.ts) and the standalone live dev server
// (`npm run dev:renderer`, vite.renderer.config.mts) so both use the same
// React root, plugins and CSP.
//
// base: the build is relative ('./') so dist/ works from file:// (Electron),
// under cmdrd's /app/ and at any root. The dev server serves under /app/ so
// cmdrd can proxy /app/ to it path-for-path (HMR WebSocket included); Vite
// redirects its own "/" to /app/.

import { resolve } from 'node:path'
import react from '@vitejs/plugin-react'
import type { Plugin, UserConfig } from 'vite'

export const DEV_HOST = '127.0.0.1'
export const DEV_PORT = 5173
export const DEV_BASE = '/app/'

// Strict CSP: no remote content; only the loopback cmdrd API. Dev adds what
// Vite's HMR and React refresh need (an inline preamble); only the dev server
// serves that page.
const connect = "connect-src 'self' ws://127.0.0.1:* http://127.0.0.1:* ws://localhost:* http://localhost:*"
const base = ["default-src 'self'", "style-src 'self' 'unsafe-inline'", "img-src 'self' data:", "font-src 'self'", connect, "object-src 'none'", "base-uri 'none'", "frame-src 'none'", "form-action 'none'"]
const prodCSP = [...base, "script-src 'self'"].join('; ')
const devCSP = [...base, "script-src 'self' 'unsafe-inline'"].join('; ')

// Vite watches only its root (src/renderer); the renderer also imports
// src/features and src/shared, so without this an edit there never hot-reloads
// and the dev server keeps serving the stale module, even on a full reload.
function watchSiblings(): Plugin {
  return {
    name: 'commander-watch-siblings',
    configureServer(server) {
      server.watcher.add(resolve(__dirname, 'src'))
    },
  }
}

function csp(): Plugin {
  let dev = false
  return {
    name: 'commander-csp',
    configResolved(c) {
      dev = c.command === 'serve'
    },
    transformIndexHtml: (html) => html.replace('%CSP%', dev ? devCSP : prodCSP),
  }
}

export function rendererConfig(command: 'serve' | 'build'): UserConfig {
  return {
    root: resolve(__dirname, 'src/renderer'),
    base: command === 'serve' ? DEV_BASE : './',
    plugins: [react(), csp(), watchSiblings()],
    // Reached directly, through cmdrd's /app/ proxy, or over
    // `ssh -L 5173:127.0.0.1:5173` from a VM.
    server: {
      host: DEV_HOST,
      port: DEV_PORT,
      allowedHosts: ['localhost', '127.0.0.1'],
      // The renderer imports features/, shared/ and test fixtures outside its root.
      fs: { allow: [resolve(__dirname, '..')] },
    },
    build: {
      outDir: resolve(__dirname, 'dist'),
      emptyOutDir: true,
      minify: true,
      rollupOptions: { input: resolve(__dirname, 'src/renderer/index.html') },
    },
  }
}
