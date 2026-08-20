// Verify the built site the way a visitor meets it.
//
// The build checks read the emitted HTML. This one serves dist/ over real HTTP
// and drives it in a browser, which is the only way to catch what actually
// breaks in production: a page that renders blank because a component threw, a
// layout that slides sideways on a phone, a nav link that 404s, a waitlist
// form that does not respond to a click.
//
// Run: node scripts/verify-site.mjs [--shots]

import { chromium } from 'playwright'
import { createServer } from 'node:http'
import { readFile, mkdir } from 'node:fs/promises'
import { existsSync } from 'node:fs'
import { join, dirname, extname } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const dist = join(root, 'dist')
const wantShots = process.argv.includes('--shots')
const shotDir = join(root, '.shots')

const TYPES = {
  '.html': 'text/html',
  '.js': 'text/javascript',
  '.css': 'text/css',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '': 'text/plain',
}

// Serve dist/ the way GitHub Pages does, including directory index resolution,
// so /blog/ has to genuinely work rather than only /blog/index.html.
const server = createServer(async (req, res) => {
  const url = (req.url ?? '/').split('?')[0]
  const candidates = url.endsWith('/')
    ? [join(dist, url, 'index.html')]
    : [join(dist, url), join(dist, `${url}.html`), join(dist, url, 'index.html')]
  for (const file of candidates) {
    if (!existsSync(file) || !file.startsWith(dist)) continue
    try {
      const body = await readFile(file)
      res.writeHead(200, { 'Content-Type': TYPES[extname(file)] ?? 'application/octet-stream' })
      res.end(body)
      return
    } catch {
      /* fall through to 404 */
    }
  }
  res.writeHead(404, { 'Content-Type': 'text/html' })
  res.end('<h1>404</h1>')
})

await new Promise((resolve) => server.listen(0, resolve))
const base = `http://localhost:${server.address().port}`

const PAGES = [
  { url: '/', name: 'home', heading: /forgets/i },
  { url: '/releases/', name: 'releases', heading: /releases/i },
  { url: '/blog/', name: 'blog', heading: /Context0/i },
  { url: '/docs/', name: 'docs', heading: /documentation/i },
]
const WIDTHS = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'mobile', width: 390, height: 844 },
]

const browser = await chromium.launch()
const failures = []
const note = (ok, message) => {
  if (!ok) failures.push(message)
}

if (wantShots) await mkdir(shotDir, { recursive: true })

for (const page of PAGES) {
  for (const vp of WIDTHS) {
    const context = await browser.newContext({
      viewport: { width: vp.width, height: vp.height },
      deviceScaleFactor: wantShots ? 2 : 1,
    })
    const tab = await context.newPage()
    const errors = []
    tab.on('pageerror', (e) => errors.push(e.message))
    tab.on('console', (m) => {
      if (m.type() === 'error') errors.push(m.text())
    })
    tab.on('response', (r) => {
      if (r.status() >= 400) errors.push(`${r.status()} for ${r.url().replace(base, '')}`)
    })

    await tab.goto(`${base}${page.url}`, { waitUntil: 'networkidle' })
    await tab.waitForTimeout(500)

    const where = `${page.name} @${vp.width}`
    note(errors.length === 0, `${where}: console/network errors: ${errors.slice(0, 3).join(' | ')}`)

    // Did React actually mount? An exception during render leaves an empty
    // #root and a page that looks like a blank dark screen.
    const info = await tab.evaluate(() => ({
      rootChildren: document.getElementById('root')?.childElementCount ?? 0,
      h1: document.querySelector('h1')?.textContent?.trim() ?? '',
      textLength: (document.body.innerText || '').trim().length,
      hasNav: !!document.querySelector('nav'),
      hasMain: !!document.querySelector('main'),
      hasFooter: !!document.querySelector('footer'),
      h1Count: document.querySelectorAll('h1').length,
    }))
    note(info.rootChildren > 0, `${where}: React did not mount - #root is empty`)
    note(info.textLength > 400, `${where}: page has almost no text (${info.textLength} chars)`)
    note(page.heading.test(info.h1), `${where}: unexpected h1 "${info.h1}"`)
    note(info.h1Count === 1, `${where}: expected exactly one h1, found ${info.h1Count}`)
    note(info.hasNav && info.hasMain && info.hasFooter, `${where}: missing a landmark element`)

    // Duplicate ids break label/aria associations silently: the second form's
    // label points at the first form's input, and a screen reader follows it.
    const dupes = await tab.evaluate(() => {
      const seen = new Map()
      for (const el of document.querySelectorAll('[id]')) {
        seen.set(el.id, (seen.get(el.id) ?? 0) + 1)
      }
      return [...seen.entries()].filter(([, n]) => n > 1).map(([id, n]) => `${id} x${n}`)
    })
    note(dupes.length === 0, `${where}: duplicate element ids: ${dupes.join(', ')}`)

    // Every input needs an accessible name, or the field is unusable by voice
    // control and unlabelled to a screen reader.
    const unlabelled = await tab.evaluate(() =>
      [...document.querySelectorAll('input, select, textarea')]
        .filter((el) => {
          if (el.type === 'hidden') return false
          if (el.getAttribute('aria-label')) return false
          if (el.getAttribute('aria-labelledby')) return false
          return !(el.id && document.querySelector(`label[for="${CSS.escape(el.id)}"]`))
        })
        .map((el) => `${el.tagName.toLowerCase()}[type=${el.type}]`),
    )
    note(unlabelled.length === 0, `${where}: inputs without labels: ${unlabelled.join(', ')}`)

    // Real horizontal scroll, not just a wide scrollWidth: body has
    // overflow-x hidden, so this measures what a user can actually do.
    const slide = await tab.evaluate(() => {
      window.scrollTo(9999, 0)
      const x = window.scrollX
      window.scrollTo(0, 0)
      return x
    })
    note(slide === 0, `${where}: page scrolls horizontally by ${slide}px`)

    // Nothing may spill past the viewport edge.
    const spill = await tab.evaluate((vw) => {
      const bad = []
      for (const el of document.querySelectorAll('body *')) {
        const r = el.getBoundingClientRect()
        if (r.width === 0 || r.height === 0) continue
        if (getComputedStyle(el).position === 'fixed') continue
        if (r.right > vw + 1) {
          let contained = false
          for (let p = el.parentElement; p; p = p.parentElement) {
            if (/auto|scroll|hidden/.test(getComputedStyle(p).overflowX)) {
              contained = true
              break
            }
          }
          if (!contained) {
            bad.push(`<${el.tagName.toLowerCase()} class="${(el.className || '').toString().slice(0, 40)}"> right=${Math.round(r.right)}`)
          }
        }
      }
      return bad.slice(0, 3)
    }, vp.width)
    note(spill.length === 0, `${where}: elements past the right edge: ${spill.join(' | ')}`)

    // Tap targets on mobile: a 20px link is not usable with a thumb.
    if (vp.width < 500) {
      const small = await tab.evaluate(() => {
        const bad = []
        for (const el of document.querySelectorAll('a, button')) {
          const r = el.getBoundingClientRect()
          if (r.width === 0 || r.height === 0) continue
          // Skip-to-content is deliberately collapsed until focused; it is
          // reached by keyboard, never by thumb.
          if (el.className.toString().includes('sr-only')) continue
          if (r.height < 24)
            bad.push(
              `${el.tagName.toLowerCase()} "${(el.textContent || '').trim().slice(0, 20)}" h=${Math.round(r.height)}`,
            )
        }
        return bad.slice(0, 3)
      })
      note(small.length === 0, `${where}: tap targets under 24px: ${small.join(' | ')}`)
    }

    if (wantShots) {
      await tab.screenshot({ path: join(shotDir, `${page.name}-${vp.name}.png`), fullPage: true })
      await tab.screenshot({ path: join(shotDir, `${page.name}-${vp.name}-fold.png`) })
    }

    await context.close()
  }
}

// Navigation actually works: click every nav link and confirm it lands.
{
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const tab = await context.newPage()
  await tab.goto(`${base}/`, { waitUntil: 'networkidle' })
  for (const label of ['Docs', 'Blog', 'Releases']) {
    await tab.goto(`${base}/`, { waitUntil: 'networkidle' })
    const link = tab.locator(`nav a:has-text("${label}")`).first()
    await link.click()
    await tab.waitForLoadState('networkidle')
    const path = new URL(tab.url()).pathname
    note(path === `/${label.toLowerCase()}/`, `nav "${label}" went to ${path}`)
    const mounted = await tab.evaluate(() => (document.getElementById('root')?.childElementCount ?? 0) > 0)
    note(mounted, `nav "${label}" landed on a page that did not mount`)
  }
  await context.close()
}

// The waitlist is the point of the site: it must be present and interactive.
{
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const tab = await context.newPage()
  await tab.goto(`${base}/`, { waitUntil: 'networkidle' })

  const email = tab.locator('input[type="email"]').first()
  note((await tab.locator('input[type="email"]').count()) > 0, 'home page has no waitlist input')
  note(
    (await tab.locator('button:has-text("Join the waitlist")').count()) > 0,
    'home page has no "Join the waitlist" button',
  )

  if ((await email.count()) > 0) {
    await email.fill('someone@example.com')
    note(
      (await email.inputValue()) === 'someone@example.com',
      'waitlist input did not accept typing',
    )
    // Submitting must produce visible feedback rather than silently doing
    // nothing, whether or not an endpoint is configured.
    await tab.locator('button:has-text("Join the waitlist")').first().click()
    await tab.waitForTimeout(600)
    const feedback = await tab.locator('main').innerText()
    note(
      /not open yet|on the list|did not go through/i.test(feedback),
      'submitting the waitlist produced no visible response',
    )
  }

  // Every page must offer the waitlist, since that is the site's one goal.
  for (const path of ['/releases/', '/blog/', '/docs/']) {
    await tab.goto(`${base}${path}`, { waitUntil: 'networkidle' })
    note(
      (await tab.locator('input[type="email"]').count()) > 0,
      `${path} has no waitlist signup`,
    )
  }

  // The hero demo toggle must actually change what is on screen.
  await tab.goto(`${base}/`, { waitUntil: 'networkidle' })
  const toggle = tab.locator('button:has-text("Replay")').first()
  if ((await toggle.count()) > 0) {
    const before = await tab.locator('main').innerText()
    await toggle.click()
    await tab.waitForTimeout(400)
    const after = await tab.locator('main').innerText()
    note(before !== after, 'hero demo toggle did not change the page')
    note(/PostgreSQL/.test(after), 'hero demo did not show the remembered answer')
  } else {
    failures.push('hero demo toggle not found')
  }
  await context.close()
}

await browser.close()
server.close()

if (failures.length > 0) {
  console.error(`\n${failures.length} problem(s):\n`)
  for (const f of failures) console.error(`  x ${f}`)
  console.error('')
  process.exit(1)
}
console.log(`OK - ${PAGES.length} pages verified in a browser at ${WIDTHS.map((w) => w.width).join('px and ')}px`)
