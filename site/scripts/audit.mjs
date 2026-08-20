// Quality checks a passing build and a happy screenshot both miss.
//
// These are the failure modes that only show up for a subset of visitors:
// someone with JavaScript blocked, a crawler that does not run scripts, a
// keyboard user, someone with reduced motion enabled, or anyone reading low
// contrast text on a dark background.
//
// Run: node scripts/audit.mjs

import { chromium } from 'playwright'
import { createServer } from 'node:http'
import { readFile } from 'node:fs/promises'
import { existsSync } from 'node:fs'
import { join, dirname, extname } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const dist = join(root, 'dist')

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
  const notFound = join(dist, '404.html')
  res.writeHead(404, { 'Content-Type': 'text/html' })
  res.end(existsSync(notFound) ? await readFile(notFound) : '<h1>404</h1>')
})
await new Promise((r) => server.listen(0, r))
const base = `http://localhost:${server.address().port}`

const PAGES = ['/', '/releases/', '/blog/', '/docs/']
const findings = []
const report = (severity, message) => findings.push({ severity, message })

const browser = await chromium.launch()

// 1. Without JavaScript.
//
// Each page is a React root, so with scripts blocked the body is empty. Social
// crawlers only read <head>, so previews survive, but a search engine has to
// render the page to see any content at all, and a visitor with JS blocked
// sees a blank screen.
{
  const context = await browser.newContext({ javaScriptEnabled: false })
  const tab = await context.newPage()
  for (const path of PAGES) {
    await tab.goto(`${base}${path}`, { waitUntil: 'domcontentloaded' })
    const text = (await tab.evaluate(() => document.body.innerText || '')).trim()
    if (text.length < 100) {
      report('high', `${path} renders ${text.length} chars of text with JS disabled`)
    }
  }
  await context.close()
}

// 2. Text contrast against the page background.
//
// WCAG AA wants 4.5:1 for body text and 3:1 for large text. The muted greys
// are exactly where this slips.
{
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const tab = await context.newPage()
  for (const path of PAGES) {
    await tab.goto(`${base}${path}`, { waitUntil: 'networkidle' })
    await tab.waitForTimeout(400)

    const low = await tab.evaluate(() => {
      const parse = (c) => {
        const m = c.match(/rgba?\(([\d.]+),\s*([\d.]+),\s*([\d.]+)(?:,\s*([\d.]+))?\)/)
        return m ? [+m[1], +m[2], +m[3], m[4] === undefined ? 1 : +m[4]] : null
      }
      const lum = ([r, g, b]) => {
        const f = (v) => {
          v /= 255
          return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4
        }
        return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b)
      }
      // Composite over the page background; a translucent colour is lighter
      // than its raw value suggests.
      const over = (fg, bg) =>
        fg[3] >= 1 ? fg : [0, 1, 2].map((i) => fg[i] * fg[3] + bg[i] * (1 - fg[3]))
      const ratio = (a, b) => {
        const [l1, l2] = [lum(a), lum(b)].sort((x, y) => y - x)
        return (l1 + 0.05) / (l2 + 0.05)
      }

      const bgOf = (el) => {
        for (let n = el; n; n = n.parentElement) {
          const c = parse(getComputedStyle(n).backgroundColor)
          if (c && c[3] > 0.9) return c
        }
        // Nothing opaque found up the tree, so the page background is what
        // shows through. Read it rather than assuming a colour, so this stays
        // correct across a palette change.
        const root = parse(getComputedStyle(document.body).backgroundColor)
        return root && root[3] > 0.9 ? root : [255, 255, 255, 1]
      }

      const out = []
      for (const el of document.querySelectorAll('p, span, a, li, h1, h2, h3, dt, dd, button, label, time')) {
        const text = (el.textContent || '').trim()
        if (!text || el.childElementCount > 0) continue
        const style = getComputedStyle(el)
        if (style.visibility === 'hidden' || style.display === 'none') continue
        // Screen-reader-only text is clipped to a 1px box and never painted, so
        // its contrast against the page is meaningless. Measuring it produced a
        // real-looking failure for the waitlist's "Email address" label, which
        // no sighted visitor can see. verify-site.mjs already skips these for
        // the same reason.
        if (el.className.toString().includes('sr-only')) continue
        if (el.closest('.sr-only')) continue
        const r = el.getBoundingClientRect()
        if (r.width === 0 || r.height === 0) continue
        // A clipped element still reports a box, so check the clip too.
        if (r.width <= 1 || r.height <= 1) continue

        const fg = parse(style.color)
        if (!fg) continue
        const bg = bgOf(el)
        const composited = over(fg, bg)
        const size = parseFloat(style.fontSize)
        const bold = parseInt(style.fontWeight, 10) >= 700
        const large = size >= 24 || (size >= 18.66 && bold)
        const need = large ? 3 : 4.5
        const got = ratio(composited, bg)
        if (got < need) {
          out.push({
            text: text.slice(0, 40),
            color: style.color,
            size: Math.round(size),
            ratio: Math.round(got * 100) / 100,
            need,
          })
        }
      }
      // De-duplicate: the same class repeated 12 times is one problem.
      const seen = new Map()
      for (const o of out) seen.set(`${o.color}|${o.size}`, o)
      return [...seen.values()]
    })

    for (const item of low) {
      report(
        'medium',
        `${path} contrast ${item.ratio}:1 (needs ${item.need}) - ${item.size}px ${item.color} - "${item.text}"`,
      )
    }
  }
  await context.close()
}

// 3. Reduced motion.
//
// The hero demo animates a fade. With the preference set it must still land on
// a meaningful end state rather than freezing mid-story.
{
  const context = await browser.newContext({
    viewport: { width: 1440, height: 900 },
    reducedMotion: 'reduce',
  })
  const tab = await context.newPage()
  await tab.goto(`${base}/`, { waitUntil: 'networkidle' })
  await tab.waitForTimeout(800)
  const text = await tab.locator('main').innerText()
  if (!/do not have enough context/i.test(text)) {
    report('high', 'with reduced motion the hero demo does not reach its end state')
  }
  const toggle = tab.locator('button:has-text("Replay")').first()
  await toggle.click()
  await tab.waitForTimeout(300)
  if (!/PostgreSQL/.test(await tab.locator('main').innerText())) {
    report('high', 'with reduced motion the hero toggle does not reveal the answer')
  }
  await context.close()
}

// 4. Keyboard reachability.
//
// Tab through the page and confirm the skip link comes first and every
// interactive control can be reached without a mouse.
{
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const tab = await context.newPage()
  await tab.goto(`${base}/`, { waitUntil: 'networkidle' })

  await tab.keyboard.press('Tab')
  const first = await tab.evaluate(() => document.activeElement?.textContent?.trim() ?? '')
  if (!/skip/i.test(first)) report('medium', `first Tab lands on "${first}", expected the skip link`)

  // Tab through and count distinct elements reached. Comparing text labels
  // would undercount, because "GitHub" and "Join the waitlist" legitimately
  // appear more than once; compare the elements themselves.
  const total = await tab.evaluate(() => {
    const els = [...document.querySelectorAll('a[href], button, input')].filter((el) => {
      const r = el.getBoundingClientRect()
      return r.width > 0 && r.height > 0
    })
    window.__focusable = els
    window.__reached = new Set()
    return els.length
  })

  for (let i = 0; i < total + 4; i++) {
    await tab.keyboard.press('Tab')
    await tab.evaluate(() => {
      const index = window.__focusable.indexOf(document.activeElement)
      if (index >= 0) window.__reached.add(index)
    })
  }
  const reached = await tab.evaluate(() => window.__reached.size)
  if (reached < total) {
    report('medium', `keyboard reached ${reached} of ${total} interactive elements`)
  }

  // Focus must be visible, or keyboard users cannot tell where they are.
  await tab.evaluate(() => document.querySelector('button, a[href]')?.focus())
  const outline = await tab.evaluate(() => {
    const s = getComputedStyle(document.activeElement)
    return `${s.outlineStyle} ${s.outlineWidth}`
  })
  if (outline.startsWith('none')) report('medium', 'focused element shows no outline')
  await context.close()
}

// 5. Scroll-revealed content that never arrives.
//
// The homepage hides several sections until they scroll into view. That is a
// nice effect and a genuinely dangerous default, because every failure mode of
// the reveal is invisible content rather than a missing animation. A full-page
// screenshot caught exactly that once: the viewport never moved, so nothing
// intersected, and three sections rendered blank.
//
// This asserts the end state that matters - after the page has settled,
// nothing marked .reveal is still transparent - both at the top of the page
// and after scrolling to the bottom.
{
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const tab = await context.newPage()
  await tab.goto(`${base}/`, { waitUntil: 'networkidle' })

  // Without scrolling at all. The failsafe in useReveal has to cover this.
  await tab.waitForTimeout(2600)
  const hiddenAtRest = await tab.evaluate(() =>
    [...document.querySelectorAll('.reveal')].filter(
      (el) => parseFloat(getComputedStyle(el).opacity) < 0.9,
    ).length,
  )
  if (hiddenAtRest > 0) {
    report('high', `${hiddenAtRest} revealed section(s) never became visible without scrolling`)
  }

  // And after a full scroll, which is the ordinary path.
  await tab.evaluate(() => window.scrollTo(0, document.body.scrollHeight))
  await tab.waitForTimeout(1200)
  const hiddenAfterScroll = await tab.evaluate(() =>
    [...document.querySelectorAll('.reveal')].filter(
      (el) => parseFloat(getComputedStyle(el).opacity) < 0.9,
    ).length,
  )
  if (hiddenAfterScroll > 0) {
    report('high', `${hiddenAfterScroll} revealed section(s) still hidden after scrolling`)
  }
  await context.close()
}

// 6. A missing page.
//
// GitHub Pages serves 404.html for any unmatched path. Without one the visitor
// gets GitHub's generic page with no way back to the site.
{
  const context = await browser.newContext()
  const tab = await context.newPage()
  const res = await tab.goto(`${base}/does-not-exist/`, { waitUntil: 'domcontentloaded' })
  const body = (await tab.evaluate(() => document.body.innerText || '')).trim()
  if (res.status() === 404 && body.length < 50) {
    report('medium', 'no custom 404 page - a mistyped URL gets a bare error with no way back')
  }
  await context.close()
}

await browser.close()
server.close()

if (findings.length === 0) {
  console.log('OK - no accessibility, no-JS, motion, keyboard, or 404 issues found')
  process.exit(0)
}

const order = { high: 0, medium: 1, low: 2 }
findings.sort((a, b) => order[a.severity] - order[b.severity])
console.log(`\n${findings.length} finding(s):\n`)
for (const f of findings) console.log(`  [${f.severity}] ${f.message}`)
console.log('')

// Findings fail the build.
//
// This previously exited 0 and only printed, which meant `pnpm check` reported
// success while listing real problems above it - including, once, a black
// heading on a black panel measuring 1:1, which is invisible text. A check
// nobody is forced to read is not a check.
process.exit(1)
