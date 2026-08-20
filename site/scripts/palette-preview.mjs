// Renders the built site under each candidate palette.
//
// The palettes differ only in the @theme token block, so rather than building
// three copies of the site, this loads the real built pages and overrides the
// tokens at runtime. What gets screenshotted is the actual site with the actual
// layout, not an approximation of it.
//
// Run: node scripts/palette-preview.mjs

import { chromium } from 'playwright'
import { createServer } from 'node:http'
import { readFile, mkdir } from 'node:fs/promises'
import { existsSync } from 'node:fs'
import { join, dirname, extname } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const dist = join(root, 'dist')
const out = join(root, '.palette-shots')
await mkdir(out, { recursive: true })

export const PALETTES = {
  a: {
    name: 'Violet Paper',
    blurb: 'The logo violet and cyan, taken darker and set on cool paper.',
    vars: {
      '--color-ink': '#f7f7fb',
      '--color-surface': '#ffffff',
      '--color-surface-2': '#ededf4',
      '--color-line': 'rgba(23, 22, 38, 0.11)',
      '--color-line-bright': 'rgba(23, 22, 38, 0.19)',
      '--color-heading': '#16151f',
      '--color-body': '#2e2c3d',
      '--color-muted': '#54536b',
      '--color-dim': '#65647c',
      '--color-brand': '#6d28d9',
      '--color-brand-deep': '#5b21b6',
      '--color-brand-ink': '#5b21b6',
      '--color-accent': '#0e7490',
      '--color-danger': '#be123c',
    },
    grid: 'rgba(23, 22, 38, 0.055)',
  },
  b: {
    name: 'Warm Editorial',
    blurb: 'Bone paper and burnt coral, in the Anthropic and Cursor register.',
    vars: {
      '--color-ink': '#fbf8f3',
      '--color-surface': '#ffffff',
      '--color-surface-2': '#f2ece1',
      '--color-line': 'rgba(38, 32, 25, 0.13)',
      '--color-line-bright': 'rgba(38, 32, 25, 0.21)',
      '--color-heading': '#1a1712',
      '--color-body': '#332e27',
      '--color-muted': '#5c554a',
      '--color-dim': '#6d6558',
      '--color-brand': '#c2410c',
      '--color-brand-deep': '#9a3412',
      '--color-brand-ink': '#9a3412',
      '--color-accent': '#0f766e',
      '--color-danger': '#be123c',
    },
    grid: 'rgba(38, 32, 25, 0.06)',
  },
  c: {
    name: 'Clear Sky',
    blurb: 'Cool white-blue base, deep sky primary, emerald for the good state.',
    vars: {
      '--color-ink': '#f5f9fd',
      '--color-surface': '#ffffff',
      '--color-surface-2': '#e7eff8',
      '--color-line': 'rgba(15, 32, 56, 0.11)',
      '--color-line-bright': 'rgba(15, 32, 56, 0.19)',
      '--color-heading': '#0f1b2d',
      '--color-body': '#26344a',
      '--color-muted': '#4c5b72',
      '--color-dim': '#5c6a81',
      '--color-brand': '#0369a1',
      '--color-brand-deep': '#075985',
      '--color-brand-ink': '#075985',
      '--color-accent': '#047857',
      '--color-danger': '#be123c',
    },
    grid: 'rgba(15, 32, 56, 0.06)',
  },
}

const TYPES = {
  '.html': 'text/html',
  '.js': 'text/javascript',
  '.css': 'text/css',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
}

const server = createServer(async (req, res) => {
  const url = (req.url ?? '/').split('?')[0]
  const candidates = url.endsWith('/')
    ? [join(dist, url, 'index.html')]
    : [join(dist, url), join(dist, url, 'index.html')]
  for (const file of candidates) {
    if (!existsSync(file) || !file.startsWith(dist)) continue
    res.writeHead(200, { 'Content-Type': TYPES[extname(file)] ?? 'application/octet-stream' })
    res.end(await readFile(file))
    return
  }
  res.writeHead(404, { 'Content-Type': 'text/html' })
  res.end('<h1>404</h1>')
})
await new Promise((r) => server.listen(0, r))
const base = `http://localhost:${server.address().port}`

const browser = await chromium.launch()

// The mark is a fixed violet in the SVG source. Under the two palettes that
// move off violet it would be the one element still wearing the old brand, so
// it is recoloured here to whatever the palette's brand is. This is a preview
// concern only: picking a non-violet palette means editing the real mark.
const apply = (palette) => {
  const style = document.createElement('style')
  const vars = Object.entries(palette.vars)
    .map(([k, v]) => `${k}: ${v};`)
    .join('\n')
  style.textContent = `
    :root { ${vars} }
    .bg-grid {
      background-image:
        linear-gradient(to right, ${palette.grid} 1px, transparent 1px),
        linear-gradient(to bottom, ${palette.grid} 1px, transparent 1px) !important;
    }
    svg path[fill="#863bff"] { fill: ${palette.vars['--color-brand']} !important; }
  `
  document.head.appendChild(style)
}

const VIEWS = [
  { label: 'desktop', width: 1440, height: 900, full: false },
  { label: 'desktop-full', width: 1440, height: 900, full: true },
  { label: 'mobile', width: 390, height: 844, full: false },
]

for (const [key, palette] of Object.entries(PALETTES)) {
  for (const view of VIEWS) {
    const context = await browser.newContext({
      viewport: { width: view.width, height: view.height },
      deviceScaleFactor: 2,
    })
    const tab = await context.newPage()
    await tab.goto(`${base}/`, { waitUntil: 'networkidle' })
    await tab.evaluate(apply, palette)
    // The hero demo fades its transcript 1.6s in; waiting past that means the
    // screenshot shows the settled state rather than a half-played animation.
    await tab.waitForTimeout(2200)
    await tab.screenshot({
      path: join(out, `${key}-${view.label}.png`),
      fullPage: view.full,
    })
    await context.close()
    console.log(`shot: ${key}-${view.label}.png (${palette.name})`)
  }
}

await browser.close()
server.close()
console.log(`\nWrote ${Object.keys(PALETTES).length * VIEWS.length} shots to .palette-shots/`)
