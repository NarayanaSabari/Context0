// Palette gate: contrast, and the absence of colour.
//
// Two independent things are enforced here.
//
// 1. The palette is strictly monochrome. Every token must be achromatic -
//    R = G = B - because "black and white" is a deliberate design decision and
//    hue creeps back in one convenient exception at a time: a red error state,
//    a green success tick, a blue link. Each looks reasonable alone and the
//    result is no longer monochrome.
//
// 2. Text still clears WCAG AA. Removing colour makes this easier to get wrong,
//    not harder, because a grey chosen for how it looks has nothing but its
//    lightness to carry contrast.
//
// scripts/audit.mjs already measures rendered text on the built pages, but it
// can only see the pairings the current pages happen to use. This reads the
// tokens themselves and checks every pairing the design intends, including
// combinations no page uses yet, so a token cannot be introduced below AA and
// then discovered later by whoever first uses it.
//
// Run: node scripts/palette.mjs

import { readFileSync, readdirSync, existsSync } from 'node:fs'
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

// Part 1: no hue anywhere.
//
// Checked before contrast, because a coloured token is a design failure whether
// or not it happens to be readable. Alpha is ignored - a translucent black is
// still achromatic - and only the RGB channels are compared.
const chromatic = []
for (const [name, value] of Object.entries(tokens)) {
  // Non-colour tokens (type sizes, spacing) live in the same block.
  if (!/^#|^rgba?\(/.test(value)) continue
  const [r, g, b] = parse(`--${name}`)
  if (r !== g || g !== b) {
    chromatic.push({ name: `--color-${name}`, value })
  }
}

if (chromatic.length > 0) {
  console.error('palette: the palette is monochrome, but these tokens carry hue\n')
  for (const c of chromatic) {
    console.error(`  ${c.name.padEnd(26)} ${c.value}`)
  }
  console.error('\nUse a neutral grey (R = G = B), or carry the meaning with weight,')
  console.error('fill, border, or an explicit label instead of colour.')
  process.exit(1)
}

// Part 2: contrast.
//
// Every surface a given text tone can land on. Text tones are checked against
// all three, because a card can sit on the page and a hover state can sit on a
// card, and the tone has to survive all of it.
const SURFACES = ['--ink', '--surface', '--surface-2']
const TEXT_TOKENS = ['--heading', '--body', '--muted', '--dim']

const checks = []
for (const text of TEXT_TOKENS) {
  for (const surface of SURFACES) {
    checks.push({ fg: text, bg: surface, need: TEXT, kind: 'text' })
  }
}

// Filled buttons and the inverted panels: the text that sits on the emphasis
// fill, in both its resting and hover tones. This is the pairing that breaks
// first when a fill is chosen for how it looks rather than for what sits on it.
checks.push({ fg: '--on-emphasis', bg: '--emphasis', need: TEXT, kind: 'button' })
checks.push({ fg: '--on-emphasis', bg: '--emphasis-soft', need: TEXT, kind: 'button' })

// The inverted panel also carries body copy at reduced opacity, and the
// waitlist renders a white-on-black button inside it. Both directions have to
// clear AA, which is the failure mode an inverted section usually hides.
checks.push({ fg: '--emphasis', bg: '--on-emphasis', need: TEXT, kind: 'inverted button' })

// The focus ring has to be visible against every surface it can appear over,
// otherwise keyboard navigation silently loses its position indicator.
for (const surface of SURFACES) {
  checks.push({ fg: '--emphasis', bg: surface, need: UI, kind: 'focus ring' })
}
// And the white ring used inside the inverted panels.
checks.push({ fg: '--on-emphasis', bg: '--emphasis', need: UI, kind: 'focus ring (inverted)' })

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

console.log(
  `palette: monochrome (no hue in ${Object.keys(tokens).length} tokens), ` +
    `${checks.length} pairings pass WCAG AA`,
)

// Part 3: no hue in the built output either.
//
// Parts 1 and 2 only see the token block. A colour written inline in a
// component - an arbitrary Tailwind value like shadow-[rgba(23,22,38,0.08)],
// or a fill baked into an inlined SVG - never passes through a token and so
// slips by. That is exactly how a tinted shadow survived the conversion.
//
// This scans the built CSS and the prerendered HTML for any colour literal
// whose channels are not equal. It runs only when dist/ exists, so the check is
// skipped on a bare source tree rather than failing confusingly.

const dist = join(root, 'dist')
if (existsSync(dist)) {
  const files = []
  const walk = (dir) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const full = join(dir, entry.name)
      if (entry.isDirectory()) walk(full)
      else if (/\.(css|html|svg)$/.test(entry.name)) files.push(full)
    }
  }
  walk(dist)

  const channels = (hex) => {
    const h = hex.length === 3 ? [...hex].map((c) => c + c).join('') : hex
    return [0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16))
  }

  const found = new Map()
  for (const file of files) {
    const text = readFileSync(file, 'utf8')
    for (const [, hex] of text.matchAll(/#([0-9a-fA-F]{6}|[0-9a-fA-F]{3})\b/g)) {
      const [r, g, b] = channels(hex.toLowerCase())
      if (r !== g || g !== b) found.set(`#${hex}`, file.slice(dist.length + 1))
    }
    for (const [, r, g, b] of text.matchAll(/rgba?\((\d+),\s*(\d+),\s*(\d+)/g)) {
      if (+r !== +g || +g !== +b) found.set(`rgb(${r},${g},${b})`, file.slice(dist.length + 1))
    }
  }

  if (found.size > 0) {
    console.error('palette: hue found in the built output\n')
    for (const [colour, file] of found) {
      console.error(`  ${colour.padEnd(24)} ${file}`)
    }
    console.error('\nThese bypass the token check because they are written inline.')
    console.error('Use a token, or a neutral grey.')
    process.exit(1)
  }
  console.log(`palette: no hue in ${files.length} built files`)
}
