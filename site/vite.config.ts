import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { resolve, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))

// Multi-page rather than a single-page app with a router.
//
// GitHub Pages serves static files: a client-side router would need the
// 404.html redirect hack to survive a hard refresh on /blog, and a visitor
// landing there would get a flash of the wrong page. Four real HTML entry
// points means /blog/ and /docs/ are genuine URLs that work on first byte,
// respond correctly to a crawler, and never depend on JavaScript to route.
//
// Each page still shares components and CSS through the normal import graph,
// so this costs nothing in duplication.
export default defineConfig({
  // Served from the apex of its own subdomain, so assets resolve from '/'.
  base: '/',
  plugins: [react(), tailwindcss()],
  build: {
    rollupOptions: {
      input: {
        main: resolve(here, 'index.html'),
        releases: resolve(here, 'releases/index.html'),
        blog: resolve(here, 'blog/index.html'),
        docs: resolve(here, 'docs/index.html'),
      },
    },
  },
  server: {
    port: 3001,
  },
})
