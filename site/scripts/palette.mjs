// Contrast gate for the design tokens.
//
// The palette is bright: dark ink on near-white paper. That direction fails
// differently from a dark theme - a colour that is comfortably readable on
// near-black is often far too light on paper, and the failure is quiet, because
// a washed-out violet still looks deliberate in a screenshot.
//
// scripts/audit.mjs already measures rendered text on the built pages, but it
// can only see the pairings the current pages happen to use. This reads the
// tokens themselves and checks every pairing the design intends, including
// combinations no page uses yet, so a token cannot be introduced below AA and
// then discovered later by whoever first uses it.
//
// Run: node scripts/palette.mjs

import { readFileSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const css = readFileSync(join(root, 'src/index.css'), 'utf8')

// Read the values straight out of the stylesheet rather than duplicating them
// here. A copy would drift, and a drifted copy that still passes is worse than
// no check at all.
const theme = css.match(/@theme\s*\{([\s\S]*?)\n\}/)
if (!theme) {
  console.error('palette: could not find the @theme block in src/index.css')
  process.exit(1)
}

const tokens = {}
for (const [, name, value] of theme[1].matchAll(/--color-([a-z0-9-]+):\s*([^;]+);/g)) {
  tokens[name] = value.trim()
}

const parse = (input) => {
  const value = input.startsWith('--') ? tokens[input.slice(2)] : input
  if (!value) throw new Error(`unknown colour: ${input}`)
  const fn = value.match(/rgba?\(([\d.]+),\s*([\d.]+),\s*([\d.]+)(?:,\s*([\d.]+))?\)/)
  if (fn) return [+fn[1], +fn[2], +fn[3], fn[4] === undefined ? 1 : +fn[4]]
  const hex = value.replace('#', '')
  const full = hex.length === 3 ? [...hex].map((c) => c + c).join('') : hex
  return [0, 2, 4].map((i) => parseInt(full.slice(i, i + 2), 16)).concat(1)
}

const luminance = (c) => {
  const channel = (v) => {
    v /= 255
    return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4
  }
  return 0.2126 * channel(c[0]) + 0.7152 * channel(c[1]) + 0.0722 * channel(c[2])
}

// Translucent tokens (the borders) are lighter than their raw value suggests,
// so they get composited over whatever they sit on before being measured.
const composite = (fg, bg) =>
  fg[3] >= 1 ? fg : [0, 1, 2].map((i) => fg[i] * fg[3] + bg[i] * (1 - fg[3])).concat(1)

const contrast = (fgToken, bgToken) => {
  const bg = parse(bgToken)
  const fg = composite(parse(fgToken), bg)
  const [hi, lo] = [luminance(fg), luminance(bg)].sort((a, b) => b - a)
  return (hi + 0.05) / (lo + 0.05)
}

// AA is 4.5:1 for normal text and 3:1 for large text and UI boundaries.
const TEXT = 4.5
const UI = 3

// Every surface a given text tone can land on. Text tones are checked against
// all three, because a card can sit on the page and a hover state can sit on a
// card, and the tone has to survive all of it.
const SURFACES = ['--ink', '--surface', '--surface-2']
const TEXT_TOKENS = ['--heading', '--body', '--muted', '--dim', '--brand-ink', '--accent', '--danger']

const checks = []
for (const text of TEXT_TOKENS) {
  for (const surface of SURFACES) {
    checks.push({ fg: text, bg: surface, need: TEXT, kind: 'text' })
  }
}

// Filled buttons: white text on the solid brand, in both resting and hover
// tones. This is the pairing that breaks first when a brand colour is chosen
// for how it looks rather than for what sits on top of it.
checks.push({ fg: '#ffffff', bg: '--brand', need: TEXT, kind: 'button' })
checks.push({ fg: '#ffffff', bg: '--brand-deep', need: TEXT, kind: 'button' })

// The focus ring has to be visible against every surface it can appear over,
// otherwise keyboard navigation silently loses its position indicator.
for (const surface of SURFACES) {
  checks.push({ fg: '--brand', bg: surface, need: UI, kind: 'focus ring' })
}

const failures = []
for (const check of checks) {
  const ratio = contrast(check.fg, check.bg)
  if (ratio < check.need) failures.push({ ...check, ratio })
}

if (failures.length > 0) {
  console.error('palette: contrast failures\n')
  for (const f of failures) {
    console.error(
      `  ${f.fg} on ${f.bg}`.padEnd(38) +
        `${f.ratio.toFixed(2)}:1  needs ${f.need}:1  (${f.kind})`,
    )
  }
  console.error('\nAdjust the token in src/index.css rather than the threshold here.')
  process.exit(1)
}

console.log(`palette: ${checks.length} token pairings pass WCAG AA`)
