// Post-build checks on the emitted site.
//
// `vite build` succeeding only means the bundler was happy. It says nothing
// about whether the pages that land on the domain are correct: a nav link
// pointing at a page that was never built, a missing CNAME that silently
// unsets the custom domain on the next deploy, or an og:image that 404s for
// every crawler all build perfectly and fail in production. These are the
// checks for the failures the build cannot see.
//
// Run via `pnpm check` locally and in the site workflow.

import { readFileSync, existsSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const dist = join(root, 'dist')
const failures = []
const check = (ok, message) => {
  if (!ok) failures.push(message)
}

if (!existsSync(dist)) {
  console.error('dist/ does not exist - run `pnpm build` first')
  process.exit(1)
}

// Everything React rendered into #root.
//
// A regex stopping at the first </div> would match the first nested close, not
// the matching one, and report an empty shell for a fully prerendered page.
// Slice from the opening marker to the module script that follows the root.
const prerenderedMarkup = (html) => {
  const start = html.indexOf('<div id="root">')
  if (start === -1) return ''
  const end = html.indexOf('<script type="module"', start)
  return html.slice(start + '<div id="root">'.length, end === -1 ? html.length : end)
}

const DOMAIN = 'context0.sabarinarayana.com'
const ORIGIN = `https://${DOMAIN}`

// Every page the site is supposed to publish, and the path it must be
// reachable at. A page silently missing from the Rollup input would otherwise
// only be noticed by a visitor hitting a 404.
const PAGES = [
  { path: 'index.html', url: '/', title: /Context0/ },
  { path: 'releases/index.html', url: '/releases/', title: /Releases/ },
  { path: 'blog/index.html', url: '/blog/', title: /Blog/ },
  { path: 'docs/index.html', url: '/docs/', title: /Docs/ },
]

// The 404 is checked separately: Pages serves it for unmatched paths, so it
// has no canonical URL of its own and must not be indexed.
const NOT_FOUND = '404.html'

// The custom domain lives in a CNAME file Pages reads from the published
// artifact. If it stops being copied, the next deploy quietly drops the domain
// back to *.github.io and every existing link breaks.
const cnamePath = join(dist, 'CNAME')
check(existsSync(cnamePath), 'dist/CNAME is missing - the custom domain would be dropped on deploy')
if (existsSync(cnamePath)) {
  const cname = readFileSync(cnamePath, 'utf8').trim()
  check(cname === DOMAIN, `dist/CNAME is "${cname}", expected ${DOMAIN}`)
}

check(existsSync(join(dist, 'og.png')), 'dist/og.png is missing - link previews would be blank')

// Cloudflare reads response headers from this file in the build output. If it
// stops being emitted the site keeps working and silently loses its CSP,
// clickjacking protection, and asset caching - a failure with no symptom.
{
  const headersPath = join(dist, '_headers')
  check(existsSync(headersPath), 'dist/_headers is missing - security headers would be dropped')
  if (existsSync(headersPath)) {
    const headers = readFileSync(headersPath, 'utf8')
    for (const name of [
      'Content-Security-Policy',
      'X-Frame-Options',
      'X-Content-Type-Options',
      'Referrer-Policy',
    ]) {
      check(headers.includes(name), `_headers does not set ${name}`)
    }
    // The CSP has to allow the one third party the pages actually load.
    check(
      /style-src[^;]*fonts\.googleapis\.com/.test(headers),
      'CSP style-src does not allow Google Fonts, which every page loads',
    )
    check(
      /font-src[^;]*fonts\.gstatic\.com/.test(headers),
      'CSP font-src does not allow Google Fonts, which every page loads',
    )
  }
}
check(existsSync(join(dist, 'favicon.svg')), 'dist/favicon.svg is missing')

for (const page of PAGES) {
  const file = join(dist, page.path)
  if (!existsSync(file)) {
    failures.push(`${page.path} was not built - ${page.url} would 404`)
    continue
  }
  const html = readFileSync(file, 'utf8')
  const where = page.url

  // Metadata that only ever fails in production: a missing description or OG
  // card looks identical in dev and renders as a bare grey box in an unfurl.
  const requiredMeta = [
    ['a title', /<title>[^<]{10,}<\/title>/],
    ['a description', /<meta[^>]+name="description"[^>]+content="[^"]{40,}"/],
    ['og:title', /<meta[^>]+property="og:title"[^>]+content="[^"]+"/],
    ['og:description', /<meta[^>]+property="og:description"[^>]+content="[^"]+"/],
    ['og:image', /<meta[^>]+property="og:image"[^>]+content="[^"]+"/],
    ['og:url', /<meta[^>]+property="og:url"[^>]+content="[^"]+"/],
    ['twitter:card', /<meta[^>]+name="twitter:card"[^>]+content="[^"]+"/],
    ['a canonical link', /<link[^>]+rel="canonical"[^>]+href="[^"]+"/],
    ['lang on <html>', /<html[^>]+lang="/],
    ['a root element for React', /id="root"/],
  ]
  for (const [name, pattern] of requiredMeta) {
    check(pattern.test(html), `${where} is missing ${name}`)
  }

  check(page.title.test(html), `${where} has an unexpected <title>`)

  // Prerendered markup. Without it the page is an empty shell until JavaScript
  // runs, so a crawler that does not execute scripts indexes nothing and a
  // visitor with JS blocked sees a blank screen.
  const rootContent = prerenderedMarkup(html)
  check(
    rootContent.length > 2000,
    `${where} has little or no prerendered markup (${rootContent.length} chars) - did prerender run?`,
  )
  check(/<h1[^>]*>/.test(rootContent), `${where} prerendered markup contains no <h1>`)

  // Canonical and og:url must point at this page, not at whichever page was
  // copy-pasted to create it. This is the classic multi-page mistake.
  const canonical = html.match(/<link[^>]+rel="canonical"[^>]+href="([^"]+)"/)?.[1]
  check(
    canonical === `${ORIGIN}${page.url}`,
    `${where} canonical is "${canonical}", expected "${ORIGIN}${page.url}"`,
  )
  const ogUrl = html.match(/<meta[^>]+property="og:url"[^>]+content="([^"]+)"/)?.[1]
  check(
    ogUrl === `${ORIGIN}${page.url}`,
    `${where} og:url is "${ogUrl}", expected "${ORIGIN}${page.url}"`,
  )

  // A relative og:image does not resolve for the crawlers that fetch it out
  // of context, and one pointing at a file that is not in dist/ is worse.
  const ogImage = html.match(/<meta[^>]+property="og:image"[^>]+content="([^"]+)"/)?.[1]
  if (ogImage) {
    check(ogImage.startsWith('https://'), `${where} og:image must be absolute, got "${ogImage}"`)
    const local = ogImage.replace(`${ORIGIN}/`, '')
    check(existsSync(join(dist, local)), `${where} og:image points at ${local}, missing from dist/`)
  }

  // Internal links must resolve to something that was actually built.
  const internal = [...html.matchAll(/href="(\/[^"#]*)"/g)].map((m) => m[1])
  for (const href of new Set(internal)) {
    const target = href.endsWith('/') ? `${href}index.html` : href
    check(
      existsSync(join(dist, target.replace(/^\//, ''))),
      `${where} links to ${href}, which does not exist in dist/`,
    )
  }

  // Any link opening a new tab needs rel="noreferrer"; without it the opened
  // page gets a handle back to this one.
  for (const tag of [...html.matchAll(/<a\b[^>]*target="_blank"[^>]*>/g)].map((m) => m[0])) {
    check(
      /rel="[^"]*noreferrer/.test(tag),
      `${where} has a target="_blank" link without rel="noreferrer": ${tag.slice(0, 100)}`,
    )
  }

  // External links: https only, and GitHub links must point at this project.
  const externals = [...html.matchAll(/href="(https?:\/\/[^"]+)"/g)].map((m) => m[1])
  for (const url of new Set(externals)) {
    check(url.startsWith('https://'), `${where} has a non-https external link: ${url}`)
  }
  for (const url of externals.filter((u) => u.includes('github.com'))) {
    check(
      url.includes('github.com/NarayanaSabari/Context0'),
      `${where} GitHub link points somewhere unexpected: ${url}`,
    )
  }

  for (const img of [...html.matchAll(/<img\b[^>]*>/g)].map((m) => m[0])) {
    check(/\balt=/.test(img), `${where} has an <img> without alt: ${img.slice(0, 100)}`)
  }
}

// The 404 page: served by Pages for any unmatched path.
{
  const file = join(dist, NOT_FOUND)
  if (!existsSync(file)) {
    failures.push('404.html was not built - a mistyped URL gets GitHub\'s generic error page')
  } else {
    const html = readFileSync(file, 'utf8')
    check(/<title>[^<]{10,}<\/title>/.test(html), '404.html has no title')
    // An error page indexed in place of real content is worse than no error page.
    check(
      /<meta[^>]+name="robots"[^>]+content="[^"]*noindex/.test(html),
      '404.html is missing noindex - search engines would index the error page',
    )
    const rootContent = prerenderedMarkup(html)
    check(rootContent.length > 500, '404.html has no prerendered markup')
    check(/href="\/"/.test(html), '404.html has no link back to the home page')
  }
}

// robots.txt and the sitemap.
//
// A sitemap that lists a page which no longer exists, or omits one that does,
// is worse than no sitemap: it teaches crawlers the wrong shape of the site.
// Keep it honest by comparing it against what was actually built.
{
  const robotsPath = join(dist, 'robots.txt')
  check(existsSync(robotsPath), 'dist/robots.txt is missing')
  if (existsSync(robotsPath)) {
    const robots = readFileSync(robotsPath, 'utf8')
    check(
      robots.includes(`Sitemap: ${ORIGIN}/sitemap.xml`),
      'robots.txt does not point at the sitemap',
    )
  }

  const sitemapPath = join(dist, 'sitemap.xml')
  check(existsSync(sitemapPath), 'dist/sitemap.xml is missing')
  if (existsSync(sitemapPath)) {
    const sitemap = readFileSync(sitemapPath, 'utf8')
    const listed = [...sitemap.matchAll(/<loc>([^<]+)<\/loc>/g)].map((m) => m[1])
    const expected = PAGES.map((p) => `${ORIGIN}${p.url}`)

    for (const url of listed) {
      check(expected.includes(url), `sitemap lists ${url}, which is not a page of this site`)
    }
    for (const url of expected) {
      check(listed.includes(url), `sitemap is missing ${url}`)
    }
    // The 404 must never be advertised for indexing.
    check(
      !listed.some((u) => u.includes('404')),
      'sitemap lists the 404 page',
    )
  }
}

// Only the origins we deliberately depend on may appear in the output.
//
// A static marketing site should have a small, known set of third parties.
// Anything new appearing here means a dependency started phoning somewhere,
// which is both a privacy question and a new way for the page to break.
{
  const ALLOWED_ORIGINS = new Set([
    ORIGIN,
    'https://github.com',
    'https://fonts.googleapis.com',
    'https://fonts.gstatic.com',
  ])
  const seen = new Set()
  for (const page of PAGES) {
    const file = join(dist, page.path)
    if (!existsSync(file)) continue
    for (const m of readFileSync(file, 'utf8').matchAll(/https?:\/\/[^"'\s)]+/g)) {
      try {
        seen.add(new URL(m[0]).origin)
      } catch {
        failures.push(`unparseable URL in ${page.url}: ${m[0]}`)
      }
    }
  }
  for (const origin of seen) {
    check(ALLOWED_ORIGINS.has(origin), `unexpected third-party origin in the output: ${origin}`)
  }
}

// The fabricated-claim guard. The project is pre-release: it has no users, no
// benchmarks, and no customers, so none of these words can be honest yet.
// Checked across the built JS too, since page copy lives in the bundle.
const bundleText = [
  ...PAGES.filter((p) => existsSync(join(dist, p.path))).map((p) =>
    readFileSync(join(dist, p.path), 'utf8'),
  ),
]
  .join(' ')
  .replace(/<(pre|code|script|style)\b[\s\S]*?<\/\1>/g, ' ')

const forbidden = [
  [/trusted by/i, '"trusted by" implies customer logos that do not exist'],
  [/\b\d+x faster\b/i, 'a speed multiple is a benchmark claim with no measurement behind it'],
  [
    /\b\d[\d,.]*\+? (?:users|customers|companies|teams|developers)\b/i,
    'a user or customer count that does not exist',
  ],
]
for (const [pattern, why] of forbidden) {
  const hit = bundleText.match(pattern)
  check(!hit, `unsupported claim "${hit?.[0]}": ${why}`)
}

if (failures.length > 0) {
  console.error(`\n${failures.length} check(s) failed:\n`)
  for (const f of failures) console.error(`  x ${f}`)
  console.error('')
  process.exit(1)
}

console.log(`OK - ${PAGES.length} pages plus 404 built and checked`)
