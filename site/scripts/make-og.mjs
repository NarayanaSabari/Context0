// Generate the Open Graph card.
//
// Rendered from HTML in the real browser rather than hand-drawn as SVG, so the
// card uses the same fonts, colors, and logo as the site itself and cannot
// drift from them.
//
// Run: node scripts/make-og.mjs

import { chromium } from 'playwright'
import { readFileSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
// The horizontal lockup, matching the one the site's nav renders. The favicon
// is square and would read as a different mark here.
const mark = `<svg viewBox="0 0 70 24" fill="#000" xmlns="http://www.w3.org/2000/svg">
  <rect x="0" y="0" width="24" height="24"/><rect x="28" y="4" width="16" height="16"/>
  <rect x="48" y="6.5" width="11" height="11"/><rect x="63" y="8.5" width="7" height="7"/>
</svg>`

const html = `<!doctype html>
<html><head><meta charset="utf-8">
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600&family=Instrument+Serif&display=swap" rel="stylesheet">
<style>
  *{margin:0;padding:0;box-sizing:border-box}
  body{
    width:1200px;height:630px;overflow:hidden;position:relative;
    background:#f7f7f7;color:#2b2b2b;
    font-family:Inter,sans-serif;display:flex;flex-direction:column;
    justify-content:space-between;padding:72px;
  }
  .grid{
    position:absolute;inset:0;
    background-image:
      linear-gradient(to right,rgba(0,0,0,.06) 1px,transparent 1px),
      linear-gradient(to bottom,rgba(0,0,0,.06) 1px,transparent 1px);
    background-size:60px 60px;
    mask-image:radial-gradient(ellipse 70% 60% at 30% 0%,#000 30%,transparent 100%);
  }
  /* A wash rather than a glow. Light bleeding out of a dark corner does not
   * translate to paper; this is a soft violet tint in the same position. */
  .glow{
    position:absolute;top:-180px;right:-120px;width:620px;height:620px;
    border-radius:50%;background:rgba(0,0,0,.05);filter:blur(130px);
  }
  .top{position:relative;display:flex;align-items:center;gap:14px}
  .top svg{height:22px;width:auto}
  .name{font-size:27px;font-weight:700;letter-spacing:-.03em;color:#000;text-transform:lowercase}
  .mid{position:relative}
  h1{
    font-family:"Instrument Serif",Georgia,serif;font-weight:400;
    font-size:92px;line-height:.98;letter-spacing:-.03em;max-width:14ch;
    color:#000;
  }
  h1 .dim{color:#525252}
  p{margin-top:26px;font-size:26px;line-height:1.5;color:#525252;max-width:30ch}
  .bot{
    position:relative;display:flex;align-items:center;gap:16px;
    font-family:ui-monospace,Menlo,monospace;font-size:17px;
    letter-spacing:.1em;text-transform:uppercase;color:#636363;
  }
  .dot{width:7px;height:7px;border-radius:50%;background:#000}
  .sep{color:rgba(0,0,0,.25)}
</style></head>
<body>
  <div class="grid"></div><div class="glow"></div>
  <div class="top">${mark}<span class="name">kora</span></div>
  <div class="mid">
    <h1>Your agent <span class="dim">forgets</span> everything.</h1>
    <p>An open-source memory engine for AI agents.</p>
  </div>
  <div class="bot">
    <span class="dot"></span><span>Open source</span>
    <span class="sep">/</span><span>Apache 2.0</span>
    <span class="sep">/</span><span>Self-hosted</span>
  </div>
</body></html>`

const browser = await chromium.launch()
const page = await browser.newPage({ viewport: { width: 1200, height: 630 } })
await page.setContent(html, { waitUntil: 'networkidle' })
// Wait for the webfont, otherwise the card renders in the fallback serif.
await page.evaluate(() => document.fonts.ready)
await page.waitForTimeout(600)
await page.screenshot({ path: join(root, 'public/og.png') })
await browser.close()

console.log('wrote public/og.png (1200x630)')
