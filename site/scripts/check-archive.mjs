// Do the archived concepts still open?
//
// site/design-archive/README.md tells the reader to open any concept directly
// in a browser. That promise is easy to break silently: the files were copied
// out of the directory they were written in, and a concept that reached for a
// sibling file, or that only ever worked because of the harness around it,
// would now fail with no warning. Nobody would notice until someone tried to
// read the design history a year from now.
//
// This opens each one from its archived location over file://, the way the
// README says to, and checks it renders something.
//
// Run: node scripts/check-archive.mjs

import { chromium } from 'playwright'
import { readdirSync, readFileSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const archive = join(root, 'design-archive')

const concepts = readdirSync(archive)
  .filter((f) => f.startsWith('concept-') && f.endsWith('.html'))
  .sort()

const failures = []
const note = (ok, message) => {
  if (!ok) failures.push(message)
}

note(concepts.length === 4, `expected 4 archived concepts, found ${concepts.length}`)

// The README table names each concept and its model. If a file is added or
// removed without updating that table, the archive starts lying about itself.
const readme = readFileSync(join(archive, 'README.md'), 'utf8')
for (const file of concepts) {
  note(readme.includes(`\`${file}\``), `${file} is not listed in design-archive/README.md`)
}
for (const m of readme.matchAll(/`(concept-[a-z]\.html)`/g)) {
  note(concepts.includes(m[1]), `README lists ${m[1]}, which is not in the archive`)
}

const browser = await chromium.launch()

for (const file of concepts) {
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const tab = await context.newPage()
  const errors = []
  tab.on('pageerror', (e) => errors.push(e.message))
  tab.on('console', (m) => {
    if (m.type() === 'error') errors.push(m.text())
  })

  await tab.goto(`file://${join(archive, file)}`, { waitUntil: 'domcontentloaded' })
  // Concepts animate on load; give them a moment to settle.
  await tab.waitForTimeout(1200)

  // Scroll the whole page before judging, and stay at the bottom. Several
  // concepts only draw their centrepiece once its section scrolls into view,
  // so a check that stays at the top never executes the code most likely to
  // be broken - concept A's canvas bug lives on exactly that path. Scrolling
  // back to the top afterwards also hides it, because the animation loop
  // stops running the affected branch, so this deliberately does not.
  await tab.evaluate(async () => {
    const step = window.innerHeight / 2
    for (let y = 0; y < document.body.scrollHeight; y += step) {
      window.scrollTo(0, y)
      await new Promise((r) => requestAnimationFrame(() => setTimeout(r, 80)))
    }
  })
  await tab.waitForTimeout(1500)

  const info = await tab.evaluate(() => ({
    text: (document.body.innerText || '').trim().length,
    hasH1: !!document.querySelector('h1'),
    // Every concept was briefed to reuse the real logo mark.
    hasSvg: document.querySelectorAll('svg').length,
    height: document.body.scrollHeight,
  }))

  note(info.text > 1000, `${file} renders only ${info.text} chars of text`)
  note(info.hasH1, `${file} has no <h1>`)
  note(info.hasSvg > 0, `${file} contains no SVG - the logo mark is missing`)
  note(info.height > 2000, `${file} is only ${info.height}px tall - it may be truncated`)

  // A concept that throws is not openable in any meaningful sense. This is
  // exactly how concept A shipped: an invalid canvas gradient that threw on
  // every frame and left its central visual blank.
  note(errors.length === 0, `${file} throws: ${errors.slice(0, 2).join(' | ')}`)

  console.log(
    `  ${file}: ${info.text} chars, ${info.height}px, ${info.hasSvg} svg` +
      (errors.length ? `, ${errors.length} errors` : ''),
  )

  await context.close()
}

await browser.close()

if (failures.length > 0) {
  console.error(`\n${failures.length} problem(s):\n`)
  for (const f of failures) console.error(`  x ${f}`)
  console.error('')
  process.exit(1)
}
console.log(`\nOK - all ${concepts.length} archived concepts open standalone`)
