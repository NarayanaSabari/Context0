// Captures the supersede demo in both lanes.
//
// The graph lane is behind a click and animates in over ~1.3s, so it never
// appears in an ordinary screenshot. This drives it the way a visitor would.
//
// Run: node scripts/shoot-demo.mjs

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
  res.writeHead(404)
  res.end('404')
})
await new Promise((r) => server.listen(0, r))
const base = `http://localhost:${server.address().port}`

const browser = await chromium.launch()

for (const view of [
  { label: 'desktop', width: 1440, height: 900 },
  { label: 'mobile', width: 390, height: 844 },
]) {
  const context = await browser.newContext({
    viewport: { width: view.width, height: view.height },
    deviceScaleFactor: 2,
  })
  const tab = await context.newPage()
  await tab.goto(`${base}/`, { waitUntil: 'networkidle' })

  const section = tab.locator('section', { hasText: 'Similar is not the same' }).first()
  await section.scrollIntoViewIfNeeded()
  await tab.waitForTimeout(900)

  await section.screenshot({ path: join(out, `demo-vector-${view.label}.png`) })

  // Switch to the graph lane and wait past the staged reveal.
  await tab.getByRole('tab', { name: 'Context0' }).click()
  await tab.waitForTimeout(1700)
  await section.screenshot({ path: join(out, `demo-graph-${view.label}.png`) })

  console.log(`shot: demo-vector-${view.label}.png, demo-graph-${view.label}.png`)
  await context.close()
}

await browser.close()
server.close()
