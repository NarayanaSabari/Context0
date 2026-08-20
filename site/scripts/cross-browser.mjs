// Cross-browser and layout-stability checks.
//
// Everything so far has been measured in Chromium, on a fast local disk, with
// fonts that resolve instantly. Three things that hides:
//
//   1. WebKit renders differently. Safari is a large share of the traffic a
//      developer-tools site gets, and it is the engine most likely to disagree
//      about mask-image, backdrop-filter, and flex sizing.
//   2. Web fonts arrive late over a real network. If the fallback metrics
//      differ from Inter and Instrument Serif, text reflows when they land -
//      the visitor sees the hero jump.
//   3. A slow connection changes what the first paint looks like.
//
// Run: node scripts/cross-browser.mjs

import { chromium, webkit } from 'playwright'
import { createServer } from 'node:http'
import { readFile, mkdir } from 'node:fs/promises'
import { existsSync } from 'node:fs'
import { join, dirname, extname } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const dist = join(root, 'dist')
const shotDir = join(root, '.shots')
const wantShots = process.argv.includes('--shots')

const TYPES = {
  '.html': 'text/html',
  '.js': 'text/javascript',
  '.css': 'text/css',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.xml': 'application/xml',
  '.txt': 'text/plain',
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
  const notFound = join(dist, '404.html')
  res.writeHead(404, { 'Content-Type': 'text/html' })
  res.end(existsSync(notFound) ? await readFile(notFound) : '<h1>404</h1>')
})
await new Promise((r) => server.listen(0, r))
const base = `http://localhost:${server.address().port}`

const PAGES = ['/', '/releases/', '/blog/', '/docs/']
const failures = []
const note = (ok, message) => {
  if (!ok) failures.push(message)
}

if (wantShots) await mkdir(shotDir, { recursive: true })

// ---------------------------------------------------------------- WebKit
{
  const browser = await webkit.launch()
  for (const width of [1440, 390]) {
    const context = await browser.newContext({ viewport: { width, height: 900 } })
    const tab = await context.newPage()
    const errors = []
    tab.on('pageerror', (e) => errors.push(e.message))
    tab.on('console', (m) => {
      if (m.type() === 'error') errors.push(m.text())
    })

    for (const path of PAGES) {
      await tab.goto(`${base}${path}`, { waitUntil: 'networkidle' })
      await tab.waitForTimeout(600)
      const where = `webkit ${path} @${width}`

      const info = await tab.evaluate(() => ({
        text: (document.body.innerText || '').trim().length,
        h1: document.querySelector('h1')?.textContent?.trim() ?? '',
        mounted: (document.getElementById('root')?.childElementCount ?? 0) > 0,
      }))
      note(info.mounted, `${where}: React did not mount`)
      note(info.text > 400, `${where}: only ${info.text} chars of text`)
      note(info.h1.length > 0, `${where}: no h1 text`)

      const slide = await tab.evaluate(() => {
        window.scrollTo(9999, 0)
        const x = window.scrollX
        window.scrollTo(0, 0)
        return x
      })
      note(slide === 0, `${where}: scrolls horizontally by ${slide}px`)

      // The grid backdrop uses mask-image. If WebKit drops it the mask covers
      // nothing and the grid runs edge to edge instead of fading out - visible,
      // but only if someone looks in Safari.
      const masked = await tab.evaluate(() => {
        const el = document.querySelector('.bg-grid')
        if (!el) return 'absent'
        const s = getComputedStyle(el)
        return s.maskImage !== 'none' || s.webkitMaskImage !== 'none' ? 'ok' : 'dropped'
      })
      note(masked !== 'dropped', `${where}: mask-image not applied on the grid backdrop`)
    }

    note(errors.length === 0, `webkit @${width}: errors: ${errors.slice(0, 3).join(' | ')}`)

    if (wantShots) {
      await tab.goto(`${base}/`, { waitUntil: 'networkidle' })
      await tab.waitForTimeout(800)
      await tab.screenshot({ path: join(shotDir, `webkit-home-${width}.png`) })
    }
    await context.close()
  }

  // The waitlist has to work in Safari too, not just render.
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const tab = await context.newPage()
  await tab.goto(`${base}/`, { waitUntil: 'networkidle' })
  const email = tab.locator('input[type="email"]').first()
  await email.fill('someone@example.com')
  note((await email.inputValue()) === 'someone@example.com', 'webkit: waitlist input rejected typing')
  const toggle = tab.locator('button:has-text("Replay")').first()
  const before = await tab.locator('main').innerText()
  await toggle.click()
  await tab.waitForTimeout(400)
  note(before !== (await tab.locator('main').innerText()), 'webkit: hero toggle did nothing')
  await context.close()
  await browser.close()
}

// -------------------------------------------------- Layout shift on fonts
//
// Load with the webfont requests stalled, measure the hero, then let them
// through and measure again. A large delta is text visibly jumping when the
// font lands.
{
  const browser = await chromium.launch()
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const tab = await context.newPage()

  let release
  const gate = new Promise((r) => {
    release = r
  })
  await tab.route('**://fonts.gstatic.com/**', async (route) => {
    await gate
    await route.continue()
  })

  await tab.goto(`${base}/`, { waitUntil: 'domcontentloaded' })
  await tab.waitForTimeout(500)
  const fallback = await tab.evaluate(() => {
    const h = document.querySelector('h1')
    const r = h.getBoundingClientRect()
    return { top: Math.round(r.top), height: Math.round(r.height) }
  })

  release()
  await tab.evaluate(() => document.fonts.ready)
  await tab.waitForTimeout(500)
  const loaded = await tab.evaluate(() => {
    const h = document.querySelector('h1')
    const r = h.getBoundingClientRect()
    return { top: Math.round(r.top), height: Math.round(r.height) }
  })

  const shift = Math.abs(loaded.height - fallback.height)
  // Some reflow is unavoidable with a display serif; a big jump is not.
  note(
    shift < 60,
    `hero shifts ${shift}px when webfonts load (fallback ${fallback.height}px -> ${loaded.height}px)`,
  )

  // Cumulative layout shift over a normal load, which is what a real user and
  // Core Web Vitals both actually experience.
  const cls = await tab.evaluate(
    () =>
      new Promise((resolve) => {
        let total = 0
        new PerformanceObserver((list) => {
          for (const entry of list.getEntries()) {
            if (!entry.hadRecentInput) total += entry.value
          }
        }).observe({ type: 'layout-shift', buffered: true })
        setTimeout(() => resolve(Math.round(total * 1000) / 1000), 900)
      }),
  )
  // Google treats 0.1 as the threshold for "good".
  note(cls < 0.1, `cumulative layout shift is ${cls}, above the 0.1 "good" threshold`)

  await context.close()
  await browser.close()
}

server.close()

if (failures.length > 0) {
  console.error(`\n${failures.length} problem(s):\n`)
  for (const f of failures) console.error(`  x ${f}`)
  console.error('')
  process.exit(1)
}
console.log('OK - renders and works in WebKit, no significant layout shift')
