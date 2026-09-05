// Bake static HTML into each page.
//
// Without this every page is an empty <div id="root"> until JavaScript runs.
// That means a visitor with scripts blocked sees a blank screen, and a crawler
// that does not execute JavaScript indexes nothing but the <head>. For a site
// whose entire job is explaining an idea and collecting an email, shipping
// zero readable text is a real defect, not a theoretical one.
//
// Vite builds the client bundles first, then this renders the same components
// to a string and injects the markup into the emitted HTML. The client
// hydrates that tree rather than replacing it.
//
// Run automatically as part of `pnpm build`.

import { renderToString } from 'react-dom/server'
import { createElement } from 'react'
import { readFile, writeFile } from 'node:fs/promises'
import { existsSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createServer } from 'vite'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
// Normally dist/. Overridable so a test can build a differently-configured
// copy somewhere else and prerender that instead.
const dist = process.env.PRERENDER_DIST ?? join(root, 'dist')

// Which built HTML file each page component belongs in.
const TARGETS = {
  main: 'index.html',
  releases: 'releases/index.html',
  '404': '404.html',
}

// A Vite dev server in middleware mode gives us the module graph, so the JSX
// and Tailwind imports resolve exactly as they do in the real build.
const vite = await createServer({
  root,
  logLevel: 'error',
  server: { middlewareMode: true },
  appType: 'custom',
})

try {
  const { pages } = await vite.ssrLoadModule('/src/entries/ssr.tsx')

  for (const [name, Component] of Object.entries(pages)) {
    const target = TARGETS[name]
    if (!target) continue
    const file = join(dist, target)
    if (!existsSync(file)) {
      console.error(`prerender: ${target} was not built`)
      process.exitCode = 1
      continue
    }

    const markup = renderToString(createElement(Component))
    const html = await readFile(file, 'utf8')

    if (!html.includes('<div id="root"></div>')) {
      console.error(`prerender: ${target} has no empty #root to fill`)
      process.exitCode = 1
      continue
    }

    await writeFile(file, html.replace('<div id="root"></div>', `<div id="root">${markup}</div>`))
    console.log(`prerender: ${target} (${(markup.length / 1024).toFixed(1)} kB of markup)`)
  }
} finally {
  await vite.close()
}
