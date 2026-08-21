// Copy docsify's runtime out of node_modules and into the docs shell.
//
// docsify's own quickstart loads the library from a CDN. This site cannot do
// that: public/_headers sets `script-src 'self'`, so a jsdelivr <script> tag is
// blocked by the browser before it executes, and the docs get stuck on their
// pre-JavaScript fallback in production while looking perfect in any local test
// that skips the headers. Verified: serving the shell with a jsdelivr script
// under the real CSP gives "Loading the script ... violates the following
// Content Security Policy directive", no .markdown-section, and only the
// hand-written fallback on screen. Relaxing the CSP to allow a third-party
// origin to execute arbitrary script on the docs domain is the wrong trade for
// saving one copy step, so the runtime is vendored instead and served from our
// own origin.
//
// Vendoring from node_modules rather than committing a blob keeps the upgrade
// path honest: `pnpm up docsify` then a rebuild, with the version recorded in
// the lockfile rather than in a filename nobody remembers to change.
//
// Runs as part of `pnpm build`, before vite, so the copied files are in
// public/ when vite copies public/ into dist/.

import { copyFile, mkdir, readFile, writeFile, rm } from 'node:fs/promises'
import { existsSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createRequire } from 'node:module'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const require = createRequire(import.meta.url)
const pkg = require('docsify/package.json')
const docsifyRoot = join(root, 'node_modules', 'docsify')
const vendor = join(root, 'public', 'docs', 'vendor')

// Only what the docs shell actually loads. Every extra file here is another
// thing served from the domain that nothing references.
const FILES = [
  ['dist/docsify.min.js', 'docsify.min.js'],
  ['dist/themes/core.min.css', 'docsify-core.min.css'],
  ['dist/plugins/search.min.js', 'search.min.js'],
]

await rm(vendor, { recursive: true, force: true })
await mkdir(vendor, { recursive: true })

for (const [from, to] of FILES) {
  const src = join(docsifyRoot, from)
  if (!existsSync(src)) {
    console.error(`sync-docsify: ${from} is missing from the installed docsify package`)
    process.exit(1)
  }
  await copyFile(src, join(vendor, to))
}

// A CDN <script> tag carries its version in the URL. A vendored copy carries
// nothing, so record it: without this, "which docsify is the site running?"
// has no answer short of diffing minified bundles.
await writeFile(
  join(vendor, 'VERSION'),
  `docsify ${pkg.version}\n\nVendored by scripts/sync-docsify.mjs from node_modules.\nDo not edit these files by hand: run \`pnpm up docsify\` and rebuild.\n`,
)

// A sourceMappingURL pointing at a .map that was deliberately not copied makes
// every devtools open in production emit a 404. verify-site.mjs treats any 404
// as a failure, and it is right to.
for (const [, name] of FILES) {
  const file = join(vendor, name)
  const body = await readFile(file, 'utf8')
  const stripped = body.replace(/\/[/*]#\s*sourceMappingURL=[^\s*]+(\s*\*\/)?/g, '')
  if (stripped !== body) await writeFile(file, stripped)
}

console.log(`sync-docsify: vendored docsify ${pkg.version} into public/docs/vendor/`)
