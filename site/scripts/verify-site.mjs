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
import { existsSync, readFileSync } from 'node:fs'
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
  // The docs are Markdown fetched at runtime by docsify.
  '.md': 'text/markdown',
}

// Apply the same response headers Cloudflare will, parsed from the _headers
// file in the build output. Without this the suite tests a site that behaves
// differently from production: a Content-Security-Policy that blocks the
// bundle or the webfonts would pass every local check and break on deploy.
//
// Every rule block is read, not just `/*`, because /docs/ deliberately
// overrides the global CSP. Reading only the global block would test the docs
// under a policy they are not actually served with, in the direction that
// hides failures rather than causing them.
const headerRules = (() => {
  const file = join(dist, '_headers')
  if (!existsSync(file)) return []
  const rules = []
  let current = null
  for (const raw of readFileSync(file, 'utf8').split('\n')) {
    const line = raw.replace(/\s+$/, '')
    if (!line.trim() || line.trim().startsWith('#')) continue
    if (/^\//.test(line)) {
      current = { pattern: line.trim(), headers: {}, removed: [] }
      rules.push(current)
      continue
    }
    if (!current) continue
    // "! Header-Name" detaches a header inherited from a broader rule.
    const detach = line.match(/^\s+!\s*([A-Za-z-]+)\s*$/)
    if (detach) {
      current.removed.push(detach[1])
      continue
    }
    const m = line.match(/^\s+([A-Za-z-]+):\s*(.+)$/)
    if (m) current.headers[m[1]] = m[2]
  }
  return rules
})()

// Cloudflare applies every matching rule in order, so a later block's detach
// plus re-add is what lets /docs/ carry a different policy from /*.
const headersFor = (url) => {
  const out = {}
  for (const rule of headerRules) {
    const re = new RegExp(`^${rule.pattern.replace(/[.+?^${}()|[\]\\]/g, '\\$&').replace(/\*/g, '.*')}$`)
    if (!re.test(url)) continue
    for (const name of rule.removed) delete out[name]
    Object.assign(out, rule.headers)
  }
  return out
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
      res.writeHead(200, {
        ...headersFor(url),
        'Content-Type': TYPES[extname(file)] ?? 'application/octet-stream',
      })
      res.end(body)
      return
    } catch {
      /* fall through to 404 */
    }
  }
  // Pages serves 404.html for anything it cannot match, with a 404 status.
  // Mirror that here so the custom error page is exercised the same way.
  const notFound = join(dist, '404.html')
  res.writeHead(404, { ...headersFor(url), 'Content-Type': 'text/html' })
  res.end(existsSync(notFound) ? await readFile(notFound) : '<h1>404</h1>')
})

await new Promise((resolve) => server.listen(0, resolve))
const base = `http://localhost:${server.address().port}`

// The React pages. /docs/ is docsify and is checked separately below: it has
// no React root, no site nav or footer, and no waitlist, so running it through
// this loop would assert things that are true of every page except that one.
const PAGES = [
  { url: '/', name: 'home', heading: /forgets/i },
  { url: '/releases/', name: 'releases', heading: /releases/i },
  { url: '/blog/', name: 'blog', heading: /kora/i },
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

    // Hydration mismatches are only warnings, so the page still works, but they
    // mean React threw away the prerendered markup and rebuilt it - which
    // defeats the point of prerendering and usually signals non-deterministic
    // render output.
    const hydrationIssue = errors.find((e) => /hydrat|did not match|server HTML/i.test(e))
    note(!hydrationIssue, `${where}: hydration mismatch: ${hydrationIssue}`)

    // A Content-Security-Policy that blocks the bundle or the webfonts looks
    // fine in dev, where no headers are applied, and breaks on deploy.
    const csp = errors.find((e) => /Content Security Policy|Refused to/i.test(e))
    note(!csp, `${where}: blocked by CSP: ${csp}`)

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
      // Scroll through before capturing. Sections that reveal on scroll have
      // never intersected the viewport in a fresh tab, so a full-page shot
      // taken straight away records them as blank.
      await tab.evaluate(async () => {
        const step = window.innerHeight * 0.75
        for (let y = 0; y < document.body.scrollHeight; y += step) {
          window.scrollTo(0, y)
          await new Promise((r) => setTimeout(r, 110))
        }
        window.scrollTo(0, 0)
        await new Promise((r) => setTimeout(r, 350))
      })
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
    // Did the destination actually render anything?
    //
    // React fills #root and leaves it in place. docsify does not: it replaces
    // that element with its own layout entirely, so "#root has children" is
    // false on /docs/ even when the page is perfect. Asking whether the body
    // has real text covers both without pretending they work the same way.
    await tab
      .waitForFunction(() => (document.body.innerText || '').trim().length > 400, undefined, {
        timeout: 5000,
      })
      .catch(() => {})
    const text = await tab.evaluate(() => (document.body.innerText || '').trim().length)
    note(text > 400, `nav "${label}" landed on a page with almost no text (${text} chars)`)
  }
  await context.close()
}

// The docs, which are docsify rather than React.
//
// This is the one page whose content arrives by fetching Markdown and
// rendering it in the browser. That gives it failure modes nothing else on the
// site has: the runtime blocked by the CSP, a Markdown file that 404s, or a
// sidebar that renders but routes nowhere. All three leave a page that loads
// successfully and is empty or broken, so they have to be clicked to be seen.
{
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const tab = await context.newPage()
  const errors = []
  tab.on('pageerror', (e) => errors.push(e.message))
  tab.on('console', (m) => {
    if (m.type() === 'error') errors.push(m.text())
  })
  tab.on('response', (r) => {
    if (r.status() >= 400) errors.push(`${r.status()} for ${r.url().replace(base, '')}`)
  })

  await tab.goto(`${base}/docs/`, { waitUntil: 'networkidle' })
  await tab.waitForTimeout(800)

  note(errors.length === 0, `docs: console/network errors: ${errors.slice(0, 3).join(' | ')}`)
  const csp = errors.find((e) => /Content Security Policy|Refused to/i.test(e))
  note(!csp, `docs: blocked by CSP: ${csp}`)

  // Did docsify actually render, or is the hand-written fallback still on
  // screen? The fallback is deliberately good enough to be mistaken for a
  // working page at a glance, so this asks for the marker docsify creates.
  const rendered = await tab.evaluate(() => ({
    hasSidebar: !!document.querySelector('.sidebar-nav'),
    hasContent: !!document.querySelector('.markdown-section'),
    h1: document.querySelector('.markdown-section h1')?.textContent?.trim() ?? '',
    textLength: (document.body.innerText || '').trim().length,
    links: document.querySelectorAll('.sidebar-nav a').length,
  }))

  note(rendered.hasContent, 'docs: docsify did not render - .markdown-section is absent')
  note(rendered.hasSidebar, 'docs: the sidebar did not render')
  note(rendered.links >= 5, `docs: sidebar has only ${rendered.links} links`)
  note(rendered.textLength > 800, `docs: page has almost no text (${rendered.textLength} chars)`)
  note(/kora/i.test(rendered.h1), `docs: unexpected h1 "${rendered.h1}"`)

  // Routing. Clicking a sidebar entry has to actually load that document,
  // rather than silently landing on docsify's own "not found" body - which is
  // what a missing Markdown file produces, with no error anywhere.
  {
    const link = tab.locator('.sidebar-nav a:has-text("Quick start")').first()
    if ((await link.count()) > 0) {
      await link.click()
      await tab.waitForTimeout(600)
      const after = await tab.evaluate(() => ({
        hash: location.hash,
        h1: document.querySelector('.markdown-section h1')?.textContent?.trim() ?? '',
        text: (document.querySelector('.markdown-section')?.innerText || '').trim(),
      }))
      note(after.hash.includes('quickstart'), `docs: quick start route is "${after.hash}"`)
      note(/quick start/i.test(after.h1), `docs: quick start rendered h1 "${after.h1}"`)
      note(
        !/not found/i.test(after.h1),
        'docs: quick start resolved to the not-found page - the Markdown file is missing',
      )
      note(after.text.length > 500, 'docs: quick start rendered almost no content')
    } else {
      failures.push('docs: no "Quick start" link in the sidebar')
    }
  }

  // Code blocks are most of these docs, and Prism highlighting them is the
  // part most likely to break quietly when the vendored bundle changes.
  {
    const code = await tab.evaluate(
      () => document.querySelectorAll('.markdown-section pre code').length,
    )
    note(code > 0, 'docs: quick start has no rendered code blocks')
  }

  // A deep link, loaded cold. This is the case hash routing exists to protect:
  // with history mode and a missing host rewrite it 404s, and only here.
  {
    await tab.goto(`${base}/docs/#/api`, { waitUntil: 'networkidle' })
    await tab.waitForTimeout(800)
    const h1 = await tab.evaluate(
      () => document.querySelector('.markdown-section h1')?.textContent?.trim() ?? '',
    )
    note(/api/i.test(h1), `docs: cold-loading a deep link rendered h1 "${h1}"`)
  }

  // Search is a plugin with its own index, and it fails by finding nothing.
  {
    await tab.goto(`${base}/docs/`, { waitUntil: 'networkidle' })
    await tab.waitForTimeout(600)
    const input = tab.locator('.sidebar input[type="search"], .sidebar .search input').first()
    if ((await input.count()) > 0) {
      await input.fill('supersede')
      await tab.waitForTimeout(700)
      const hits = await tab.evaluate(
        () => document.querySelectorAll('.results-panel .matching-post').length,
      )
      note(hits > 0, 'docs: searching for "supersede" returned nothing')
    } else {
      failures.push('docs: the search input did not render')
    }
  }

  // No colour, measured after the cascade.
  //
  // scripts/palette.mjs enforces this everywhere else by scanning the built
  // CSS, and deliberately skips docs/vendor/: docsify's stylesheet is
  // third-party, is full of hue - an accent blue plus a six-colour Prism
  // theme - and is vendored verbatim so upgrades stay a one-line change. What
  // makes that skip safe is this check, which asks the browser for computed
  // colours and so sees whether docs/theme.css actually won.
  //
  // Every route is visited, and search is left open, because the hue that
  // survived the first pass of the theme was in a <mark> that only exists once
  // a search has run.
  {
    const scan = () =>
      tab.evaluate(() => {
        const parse = (c) => {
          const m = (c || '').match(/rgba?\((\d+),\s*(\d+),\s*(\d+)(?:,\s*([\d.]+))?/)
          return m ? [+m[1], +m[2], +m[3], m[4] === undefined ? 1 : +m[4]] : null
        }
        const out = new Set()
        for (const el of document.querySelectorAll('*')) {
          const style = getComputedStyle(el)
          if (style.visibility === 'hidden' || style.display === 'none') continue
          const box = el.getBoundingClientRect()
          if (box.width < 1 || box.height < 1) continue
          for (const prop of [
            'color',
            'backgroundColor',
            'borderTopColor',
            'borderBottomColor',
            'borderLeftColor',
            'fill',
          ]) {
            const v = parse(style[prop])
            if (!v) continue
            const [r, g, b, a] = v
            if (a === 0) continue
            if (r !== g || g !== b) {
              out.add(`${prop}: ${style[prop]} on <${el.tagName.toLowerCase()}>`)
            }
          }
        }
        return [...out]
      })

    for (const route of ['#/', '#/quickstart', '#/api', '#/concepts', '#/operations']) {
      await tab.goto(`${base}/docs/${route}`, { waitUntil: 'networkidle' })
      await tab.waitForTimeout(700)
      const hues = await scan()
      note(hues.length === 0, `docs ${route}: colour on screen: ${hues.slice(0, 3).join(' | ')}`)
    }

    // And again with search results showing, which is markup no route renders
    // on its own.
    const input = tab.locator('.sidebar input[type="search"], .sidebar .search input').first()
    if ((await input.count()) > 0) {
      await input.fill('memory')
      await tab.waitForTimeout(800)
      const hues = await scan()
      note(hues.length === 0, `docs search results: colour on screen: ${hues.slice(0, 3).join(' | ')}`)
    }
  }

  // Narrow viewport.
  //
  // Everything above runs at 1440px, where the sidebar is docked and the
  // content column is wide. A phone is a different layout: the sidebar
  // collapses behind a toggle, and any element that cannot shrink pushes the
  // page sideways. These docs are mostly reference tables, which is exactly
  // the element that does not shrink - and a theme rule that broke this
  // reproduced only on CI, because whether the widest table exceeds 390px
  // depends on the font metrics of the machine rendering it.
  {
    const narrow = await browser.newContext({ viewport: { width: 390, height: 844 } })
    const phone = await narrow.newPage()

    for (const route of ['#/', '#/api', '#/configuration', '#/operations']) {
      await phone.goto(`${base}/docs/${route}`, { waitUntil: 'networkidle' })
      await phone.waitForTimeout(700)

      // Real horizontal scroll, measured the way a thumb would produce it.
      const slide = await phone.evaluate(() => {
        window.scrollTo(9999, 0)
        const x = window.scrollX
        window.scrollTo(0, 0)
        return x
      })
      note(slide === 0, `docs ${route} @390: page scrolls horizontally by ${slide}px`)

      // A table wider than the screen is fine, as long as it scrolls inside
      // its own box rather than taking the page with it.
      const spill = await phone.evaluate(
        (vw) =>
          [...document.querySelectorAll('.markdown-section table')]
            .map((el) => Math.round(el.getBoundingClientRect().right))
            .filter((right) => right > vw + 1).length,
        390,
      )
      note(spill === 0, `docs ${route} @390: ${spill} table(s) overflow the viewport`)
    }

    // The sidebar toggle is the only route to navigation on a phone.
    await phone.goto(`${base}/docs/`, { waitUntil: 'networkidle' })
    await phone.waitForTimeout(600)
    const toggle = phone.locator('.sidebar-toggle').first()
    if ((await toggle.count()) > 0) {
      await toggle.click()
      await phone.waitForTimeout(500)
      const visible = await phone.evaluate(() => {
        const el = document.querySelector('.sidebar')
        if (!el) return false
        const box = el.getBoundingClientRect()
        return box.width > 0 && box.right > 0
      })
      note(visible, 'docs @390: the sidebar toggle did not reveal the navigation')
    } else {
      failures.push('docs @390: no sidebar toggle, so navigation is unreachable on a phone')
    }

    await narrow.close()
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

  // Every React page must offer the waitlist, since that is the site's one
  // goal. /docs/ is excluded: it is docsify, and its job is documentation.
  for (const path of ['/releases/', '/blog/']) {
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

// The 404 page, served the way Pages serves it: for a path that does not
// exist. This is the one page nobody tests by hand, because reaching it
// requires deliberately mistyping a URL.
{
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const tab = await context.newPage()
  const errors = []
  tab.on('pageerror', (e) => errors.push(e.message))
  tab.on('response', (r) => {
    // The document itself is expected to 404; its assets are not.
    if (r.status() >= 400 && !r.url().endsWith('/no-such-page/')) {
      errors.push(`${r.status()} for ${r.url().replace(base, '')}`)
    }
  })

  const response = await tab.goto(`${base}/no-such-page/`, { waitUntil: 'networkidle' })
  note(response.status() === 404, `unknown path returned ${response.status()}, expected 404`)
  note(errors.length === 0, `404 page errors: ${errors.slice(0, 3).join(' | ')}`)

  const text = await tab.evaluate(() => document.body.innerText || '')
  note(text.length > 200, `404 page has almost no text (${text.length} chars)`)
  note(/does not exist/i.test(text), '404 page does not say the page is missing')

  // Styling actually applied: an unstyled 404 is the classic broken-asset
  // symptom, and it looks like the site is down rather than the URL wrong.
  // Asserted as "the stylesheet applied", not as a specific colour: pinning the
  // exact page background here meant a palette change failed this check for the
  // wrong reason. An unstyled page is transparent or plain white with the
  // default serif stack, so that is what this looks for.
  const styled = await tab.evaluate(() => {
    const style = getComputedStyle(document.body)
    const painted =
      style.backgroundColor !== 'rgba(0, 0, 0, 0)' &&
      style.backgroundColor !== 'transparent'
    return painted && style.fontFamily.includes('Inter')
  })
  note(styled, '404 page did not load the stylesheet')

  // A way back is the entire point of a custom 404.
  note((await tab.locator('a[href="/"]').count()) > 0, '404 page has no link home')
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
