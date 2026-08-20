// Do the outbound links actually resolve?
//
// Kept separate from the other checks, and off by default, because it depends
// on the network and on GitHub being up. A marketing site's build must not go
// red because github.com had a bad minute. But a link to a file that was
// renamed or deleted is a real defect, and nothing else in the pipeline can
// see it: every other check only proves the URL is well-formed.
//
// Run manually, or on a schedule:  node scripts/check-links.mjs

import { readFileSync, existsSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const dist = join(root, 'dist')

if (!existsSync(dist)) {
  console.error('dist/ does not exist - run `pnpm build` first')
  process.exit(1)
}

const PAGES = ['index.html', 'releases/index.html', 'blog/index.html', 'docs/index.html', '404.html']

// Collect every external link, remembering which pages each appears on so a
// failure says where to go and fix it.
const links = new Map()
for (const page of PAGES) {
  const file = join(dist, page)
  if (!existsSync(file)) continue
  const html = readFileSync(file, 'utf8')
  for (const m of html.matchAll(/href="(https?:\/\/[^"]+)"/g)) {
    const url = m[1]
    // Font stylesheets are exercised by every page load already.
    if (url.includes('fonts.googleapis.com') || url.includes('fonts.gstatic.com')) continue
    if (url.startsWith('https://context0.sabarinarayanakg.in')) continue
    if (!links.has(url)) links.set(url, new Set())
    links.get(url).add(page)
  }
}

if (links.size === 0) {
  console.log('no external links to check')
  process.exit(0)
}

const failures = []
const results = await Promise.all(
  [...links.entries()].map(async ([url, pages]) => {
    try {
      const controller = new AbortController()
      const timer = setTimeout(() => controller.abort(), 20000)
      // HEAD first; some hosts answer it poorly, so fall back to GET.
      let res = await fetch(url, {
        method: 'HEAD',
        redirect: 'follow',
        signal: controller.signal,
      })
      if (res.status === 405 || res.status === 403) {
        res = await fetch(url, { redirect: 'follow', signal: controller.signal })
      }
      clearTimeout(timer)
      return { url, pages, status: res.status, finalUrl: res.url }
    } catch (error) {
      return { url, pages, status: 0, error: String(error).slice(0, 80) }
    }
  }),
)

for (const r of results) {
  const where = [...r.pages].join(', ')
  if (r.status === 0) {
    failures.push(`${r.url} did not respond (${r.error}) - linked from ${where}`)
  } else if (r.status >= 400) {
    failures.push(`${r.url} returned ${r.status} - linked from ${where}`)
  } else {
    console.log(`  ${r.status}  ${r.url}`)
  }
}

if (failures.length > 0) {
  console.error(`\n${failures.length} broken link(s):\n`)
  for (const f of failures) console.error(`  x ${f}`)
  console.error('')
  process.exit(1)
}

console.log(`\nOK - ${results.length} external links resolve`)
