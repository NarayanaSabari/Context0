// The waitlist under conditions that are not the happy path.
//
// The waitlist is the only thing on this site that does anything, and the only
// reason the site exists. Every other check confirms it renders. These confirm
// it behaves when the provider is slow, down, or rejecting - and, most
// importantly, that it never tells someone they are on a list when they are
// not.
//
// The configured path needs a real build, because Vite inlines the endpoint
// constant and string-patching the bundle is brittle. So this builds a second
// copy of the site into a temporary directory with an endpoint set, and points
// that endpoint at a stub the test controls. No provider account, no network.
//
// Run: node scripts/waitlist.mjs

import { chromium } from 'playwright'
import { createServer } from 'node:http'
import { readFile, writeFile, rm, mkdtemp } from 'node:fs/promises'
import { existsSync } from 'node:fs'
import { execFileSync } from 'node:child_process'
import { tmpdir } from 'node:os'
import { join, dirname, extname } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const configPath = join(root, 'src/config.ts')
const STUB = 'https://waitlist.test/subscribe'

const TYPES = {
  '.html': 'text/html',
  '.js': 'text/javascript',
  '.css': 'text/css',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
}

function serve(dir) {
  const server = createServer(async (req, res) => {
    const url = (req.url ?? '/').split('?')[0]
    for (const file of url.endsWith('/')
      ? [join(dir, url, 'index.html')]
      : [join(dir, url), join(dir, url, 'index.html')]) {
      if (!existsSync(file) || !file.startsWith(dir)) continue
      res.writeHead(200, { 'Content-Type': TYPES[extname(file)] ?? 'application/octet-stream' })
      res.end(await readFile(file))
      return
    }
    res.writeHead(404, { 'Content-Type': 'text/html' })
    res.end('<h1>404</h1>')
  })
  return server
}

const failures = []
const note = (ok, message) => {
  if (!ok) failures.push(message)
}

// Build a configured copy. The original config is restored in `finally`, so an
// aborted run cannot leave a stub endpoint in the working tree.
const original = await readFile(configPath, 'utf8')
const outDir = await mkdtemp(join(tmpdir(), 'ctx0-waitlist-'))
let browser

try {
  await writeFile(
    configPath,
    original.replace("export const WAITLIST_ENDPOINT = ''", `export const WAITLIST_ENDPOINT = '${STUB}'`),
  )
  execFileSync('npx', ['vite', 'build', '--outDir', outDir, '--emptyOutDir'], {
    cwd: root,
    stdio: 'pipe',
  })
  execFileSync('node', ['scripts/prerender.mjs'], {
    cwd: root,
    stdio: 'pipe',
    env: { ...process.env, PRERENDER_DIST: outDir },
  })
} finally {
  await writeFile(configPath, original)
}

const server = serve(outDir)
await new Promise((r) => server.listen(0, r))
const base = `http://localhost:${server.address().port}`

browser = await chromium.launch()

async function open(stubHandler) {
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 } })
  const tab = await context.newPage()
  if (stubHandler) await tab.route(`${STUB}**`, stubHandler)
  await tab.goto(`${base}/`, { waitUntil: 'networkidle' })
  await tab.waitForTimeout(300)
  return { context, tab }
}

async function submit(tab, email = 'someone@example.com') {
  await tab.locator('input[type="email"]').first().fill(email)
  await tab.locator('button:has-text("Join the waitlist")').first().click()
}

// 1. The provider returns 500.
{
  const { context, tab } = await open((route) => route.fulfill({ status: 500, body: 'nope' }))
  await submit(tab)
  await tab.waitForTimeout(700)
  const text = await tab.locator('main').innerText()
  note(!/on the list/i.test(text), 'a 500 from the provider still showed a success message')
  note(/did not go through/i.test(text), 'a 500 from the provider produced no visible error')
  await context.close()
}

// 2. The network drops entirely.
{
  const { context, tab } = await open((route) => route.abort('failed'))
  await submit(tab)
  await tab.waitForTimeout(700)
  const text = await tab.locator('main').innerText()
  note(!/on the list/i.test(text), 'a network failure still showed a success message')
  note(/did not go through/i.test(text), 'a network failure produced no visible error')
  await context.close()
}

// 3. The provider is slow: the controls must lock while in flight.
{
  let release
  const held = new Promise((r) => {
    release = r
  })
  const { context, tab } = await open(async (route) => {
    await held
    await route.fulfill({ status: 200, body: '{}' })
  })
  await submit(tab)
  await tab.waitForTimeout(400)

  const pending = tab.locator('button:has-text("Joining")').first()
  const showsPending = (await pending.count()) > 0
  note(showsPending, 'a slow request did not show a pending state')
  if (showsPending) {
    note(await pending.isDisabled(), 'the button stayed clickable during a slow request')
    note(
      await tab.locator('input[type="email"]').first().isDisabled(),
      'the input stayed editable during a slow request',
    )
  }

  release()
  await tab.waitForTimeout(800)
  note(
    /on the list/i.test(await tab.locator('main').innerText()),
    'a successful response did not show the confirmation',
  )
  await context.close()
}

// 4. Invalid input must never reach the provider.
{
  let requests = 0
  const { context, tab } = await open((route) => {
    requests += 1
    return route.fulfill({ status: 200, body: '{}' })
  })
  await tab.locator('input[type="email"]').first().fill('not-an-email')
  await tab.locator('button:has-text("Join the waitlist")').first().click()
  await tab.waitForTimeout(600)
  note(requests === 0, `an invalid address was sent to the provider (${requests} requests)`)
  note(
    !(await tab.evaluate(() => document.querySelector('input[type="email"]').checkValidity())),
    'the browser considers "not-an-email" valid',
  )
  await context.close()
}

// 5. The address is sent under the field name the provider expects.
{
  let sent = null
  const { context, tab } = await open(async (route) => {
    sent = route.request().postData()
    await route.fulfill({ status: 200, body: '{}' })
  })
  await submit(tab, 'someone@example.com')
  await tab.waitForTimeout(700)
  note(sent !== null, 'the configured form sent no request at all')
  note(
    sent === null || /name="email"/.test(sent),
    `the request did not carry an "email" field: ${String(sent).slice(0, 120)}`,
  )
  note(
    sent === null || sent.includes('someone@example.com'),
    'the request did not carry the address the visitor typed',
  )
  await context.close()
}

await browser.close()
server.close()
await rm(outDir, { recursive: true, force: true })

// 6. Unconfigured, which is how the site ships today. Checked against the
//    normal build, since that is the artifact that gets deployed.
{
  const shipped = join(root, 'dist')
  if (existsSync(join(shipped, 'index.html'))) {
    const s2 = serve(shipped)
    await new Promise((r) => s2.listen(0, r))
    const b2 = await chromium.launch()
    const tab = await b2.newPage()
    let outbound = 0
    tab.on('request', (r) => {
      if (!r.url().startsWith(`http://localhost:${s2.address().port}`) && !r.url().includes('fonts.g')) {
        outbound += 1
      }
    })
    await tab.goto(`http://localhost:${s2.address().port}/`, { waitUntil: 'networkidle' })
    await submit(tab)
    await tab.waitForTimeout(700)
    const text = await tab.locator('main').innerText()
    note(!/on the list/i.test(text), 'the unconfigured form claimed the visitor was on the list')
    note(/not open yet/i.test(text), 'the unconfigured form gave no explanation')
    note(outbound === 0, `the unconfigured form sent ${outbound} outbound requests`)
    note(
      (await tab.locator('a:has-text("Open GitHub")').count()) > 0,
      'the unconfigured form offered no alternative',
    )
    await b2.close()
    s2.close()
  } else {
    failures.push('dist/ is missing - run `pnpm build` before this check')
  }
}

if (failures.length > 0) {
  console.error(`\n${failures.length} problem(s):\n`)
  for (const f of failures) console.error(`  x ${f}`)
  console.error('')
  process.exit(1)
}
console.log('OK - waitlist handles failure, latency, invalid input, and no configuration')
