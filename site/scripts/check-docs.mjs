// Checks for the docsify docs at /docs/.
//
// The rest of the site is React, prerendered at build time, and checked by
// check-build.mjs. The docs are neither: docsify fetches Markdown and renders
// it in the browser. That difference means the failures worth catching here
// are different too, and none of them are visible to a build that succeeded:
//
//   - A sidebar entry pointing at a Markdown file that does not exist. docsify
//     renders the "not found" page for it; nothing errors, and the link looks
//     fine until someone clicks it.
//   - The vendored runtime missing, because sync-docsify.mjs did not run. The
//     page still serves, and shows the fallback markup forever.
//   - A <script> or <link> pointing at a CDN. It works locally, where the CSP
//     is not applied, and is blocked in production, where it is.
//   - The no-JavaScript fallback disappearing. Every other page is prerendered
//     and check-build.mjs asserts that; this page's equivalent is written by
//     hand, so nothing else would notice it being emptied.
//
// Run via `pnpm docs`, and in `pnpm check`.

import { readFileSync, existsSync, readdirSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const dist = join(root, 'dist')
const docs = join(dist, 'docs')

const failures = []
const check = (ok, message) => {
  if (!ok) failures.push(message)
}

if (!existsSync(dist)) {
  console.error('dist/ does not exist - run `pnpm build` first')
  process.exit(1)
}

check(existsSync(docs), 'dist/docs/ is missing - /docs/ would 404')
if (!existsSync(docs)) {
  console.error(failures.join('\n'))
  process.exit(1)
}

// 1. The shell and the vendored runtime.
//
// The runtime is copied out of node_modules at build time rather than
// committed, so "did that copy actually happen" is a real question with a
// silent failure mode: the page still loads and shows its fallback forever.
const REQUIRED = [
  'index.html',
  'config.js',
  'theme.css',
  'vendor/docsify.min.js',
  'vendor/docsify-core.min.css',
  'vendor/search.min.js',
  'vendor/VERSION',
]
for (const file of REQUIRED) {
  check(existsSync(join(docs, file)), `dist/docs/${file} is missing`)
}

const html = existsSync(join(docs, 'index.html'))
  ? readFileSync(join(docs, 'index.html'), 'utf8')
  : ''

// Comments are stripped for the checks that scan for markup, because this file
// is heavily commented and several of those comments discuss the very tags
// being searched for. Matching the word "<script>" inside a comment explaining
// why there is no inline script would be a memorable way to waste an hour.
const stripped = html.replace(/<!--[\s\S]*?-->/g, '')

// 2. Nothing may be loaded from a third-party origin.
//
// public/_headers sets `script-src 'self'`, so a CDN <script> - which is what
// docsify's own quickstart tells you to write - is blocked by the browser in
// production while working perfectly in any local check that skips the
// headers. Google Fonts is the one allowed exception, and the CSP names it.
{
  const external = [...stripped.matchAll(/(?:src|href)="(https?:\/\/[^"]+)"/g)].map((m) => m[1])
  for (const url of external) {
    const allowed =
      url.startsWith('https://fonts.googleapis.com') ||
      url.startsWith('https://fonts.gstatic.com') ||
      url.startsWith('https://kora.sabarinarayana.com') ||
      url.startsWith('https://github.com/NarayanaSabari/Kora')
    check(allowed, `/docs/ loads or links to an unexpected third party: ${url}`)
  }

  // Belt and braces: catch a CDN reference anywhere in the file, including in
  // the docsify config, which is not an attribute and so escapes the check
  // above.
  for (const cdn of ['cdn.jsdelivr.net', 'unpkg.com', 'cdnjs.cloudflare.com']) {
    check(!stripped.includes(cdn), `/docs/index.html references ${cdn}; the CSP would block it`)
  }
}

// 3. No inline script.
//
// Same reason: the CSP has no 'unsafe-inline', and docsify's documented setup
// puts window.$docsify in an inline block.
check(
  !/<script(?![^>]*\bsrc=)[^>]*>[\s\S]*?\S[\s\S]*?<\/script>/.test(stripped),
  '/docs/index.html contains an inline <script>, which the CSP blocks; move it into config.js',
)

// 4. The pre-JavaScript fallback.
//
// docsify replaces #root once it boots, so this markup is what a crawler that
// does not run scripts, and a reader with scripts blocked, actually get. The
// threshold is lower than check-build.mjs uses for prerendered React pages,
// because this is written by hand and is a summary rather than a whole page.
{
  const start = stripped.indexOf('<div id="root">')
  const end = stripped.indexOf('<script', start === -1 ? 0 : start)
  const fallback = start === -1 ? '' : stripped.slice(start, end === -1 ? stripped.length : end)
  const text = fallback
    .replace(/<[^>]+>/g, ' ')
    .replace(/\s+/g, ' ')
    .trim()

  check(start !== -1, '/docs/index.html has no #root element for docsify to render into')
  check(
    text.length > 600,
    `/docs/ has only ${text.length} chars of no-JavaScript fallback text - a crawler would see almost nothing`,
  )
  check(/<h1[^>]*>/.test(fallback), '/docs/ fallback markup contains no <h1>')
  check(
    /github\.com\/NarayanaSabari\/Kora/.test(fallback),
    '/docs/ fallback does not link to the repository, which is where the docs are readable without JavaScript',
  )
}

// 5. Metadata, matching what check-build.mjs demands of every other page.
{
  const ORIGIN = 'https://kora.sabarinarayana.com'
  const required = [
    ['a title', /<title>[^<]{10,}<\/title>/],
    ['a description', /<meta[^>]+name="description"[^>]+content="[^"]{40,}"/],
    ['og:title', /<meta[^>]+property="og:title"[^>]+content="[^"]+"/],
    ['og:image', /<meta[^>]+property="og:image"[^>]+content="[^"]+"/],
    ['twitter:card', /<meta[^>]+name="twitter:card"[^>]+content="[^"]+"/],
    ['lang on <html>', /<html[^>]+lang="/],
  ]
  for (const [name, pattern] of required) {
    check(pattern.test(html), `/docs/ is missing ${name}`)
  }

  const canonical = html.match(/<link[^>]+rel="canonical"[^>]+href="([^"]+)"/)?.[1]
  check(
    canonical === `${ORIGIN}/docs/`,
    `/docs/ canonical is "${canonical}", expected "${ORIGIN}/docs/"`,
  )
  const ogUrl = html.match(/<meta[^>]+property="og:url"[^>]+content="([^"]+)"/)?.[1]
  check(ogUrl === `${ORIGIN}/docs/`, `/docs/ og:url is "${ogUrl}", expected "${ORIGIN}/docs/"`)

  for (const tag of [...stripped.matchAll(/<a\b[^>]*target="_blank"[^>]*>/g)].map((m) => m[0])) {
    check(/rel="[^"]*noreferrer/.test(tag), `/docs/ has a target="_blank" link without noreferrer`)
  }
}

// 6. Sidebar entries must resolve to files that exist.
//
// This is the failure this script exists for. docsify silently renders its
// "not found" page for a missing document: the build passes, the sidebar looks
// complete, and the gap only appears when a reader clicks the link.
const pages = readdirSync(docs).filter((f) => f.endsWith('.md'))
check(pages.length > 0, 'dist/docs/ contains no Markdown at all')

{
  const sidebarPath = join(docs, '_sidebar.md')
  check(existsSync(sidebarPath), 'dist/docs/_sidebar.md is missing - the docs would have no nav')

  if (existsSync(sidebarPath)) {
    const sidebar = readFileSync(sidebarPath, 'utf8')
    const links = [...sidebar.matchAll(/\[[^\]]+\]\(([^)]+)\)/g)].map((m) => m[1])
    check(links.length > 0, '_sidebar.md lists no pages')

    for (const link of links) {
      if (link.startsWith('http') || link === '/') continue
      check(existsSync(join(docs, link)), `_sidebar.md links to ${link}, which is not in dist/docs/`)
    }

    // Every Markdown page should be reachable from the sidebar. An orphan is
    // not broken, but it is unfindable, which for documentation is the same
    // thing. README and the 404 body are reached by docsify directly.
    const linked = new Set(links.map((l) => l.replace(/^\.\//, '')))
    for (const page of pages) {
      if (page === 'README.md' || page === '_sidebar.md' || page === 'not-found.md') continue
      check(linked.has(page), `dist/docs/${page} is not linked from _sidebar.md; nobody would find it`)
    }
  }
}

// 7. Cross-links between docs pages must resolve.
//
// Same silent failure as the sidebar, and more likely: these are written
// inline while drafting a page, and a renamed file leaves them behind.
for (const page of pages) {
  const body = readFileSync(join(docs, page), 'utf8')
  for (const m of body.matchAll(/\[[^\]]+\]\(([^)\s]+)\)/g)) {
    const target = m[1]
    if (target.startsWith('http') || target.startsWith('#') || target === '/') continue
    const [file] = target.split('#')
    if (!file) continue
    check(existsSync(join(docs, file)), `docs/${page} links to ${file}, which does not exist`)
  }
}

// 8. The docsify config has to name a homepage and sidebar that exist, since
// a typo there empties the docs without failing anything.
{
  const configPath = join(docs, 'config.js')
  if (existsSync(configPath)) {
    const config = readFileSync(configPath, 'utf8')
    check(/basePath:\s*'\/docs\/'/.test(config), "docs config does not set basePath to '/docs/'")
    check(/loadSidebar:\s*true/.test(config), 'docs config does not load the sidebar')
    const homepage = config.match(/homepage:\s*'([^']+)'/)?.[1]
    check(
      !homepage || existsSync(join(docs, homepage)),
      `docs config homepage is "${homepage}", which is not in dist/docs/`,
    )
    const notFound = config.match(/notFoundPage:\s*'([^']+)'/)?.[1]
    check(
      !notFound || existsSync(join(docs, notFound)),
      `docs config notFoundPage is "${notFound}", which is not in dist/docs/`,
    )
  }
}

if (failures.length > 0) {
  console.error(`\ndocs check failed (${failures.length}):\n`)
  for (const f of failures) console.error(`  - ${f}`)
  console.error('')
  process.exit(1)
}

const version = existsSync(join(docs, 'vendor/VERSION'))
  ? readFileSync(join(docs, 'vendor/VERSION'), 'utf8').split('\n')[0]
  : 'unknown'
console.log(`docs: ${pages.length} pages, ${version}, all links resolve`)
