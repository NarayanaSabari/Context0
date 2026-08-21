# Authoring the kora docs

The documentation at [kora.sabarinarayana.com/docs/](https://kora.sabarinarayana.com/docs/), built with [docsify](https://docsify.js.org/).

## Adding a page

1. Write the Markdown here, in this directory.
2. Add a link to it in `_sidebar.md`.

That is the whole process. There is no build step for content: docsify fetches the Markdown at runtime, so a new file is live as soon as it deploys.

The second step is not optional. `scripts/check-docs.mjs` fails the build for a page that no sidebar entry links to, on the grounds that documentation nobody can find is documentation nobody reads.

## Running it locally

```bash
pnpm --filter site dev     # then open /docs/
```

Editing Markdown needs no rebuild, just a refresh. Editing `index.html`, `config.js`, or `theme.css` does need one, because those are copied out of `public/`.

## Checking it

```bash
pnpm docs      # links, sidebar coverage, vendored runtime, CSP, fallback
pnpm check     # the above plus the whole site suite, in three browsers
```

`pnpm docs` is fast and catches the things that fail silently: a sidebar entry pointing at a file that does not exist, a cross-link to a renamed page, a missing vendored runtime.

## How this is wired, and why

Three decisions here are load-bearing, and all three exist to avoid failures that appear only in production.

### docsify is vendored, not loaded from a CDN

`public/_headers` sets `script-src 'self'`. The `<script src="https://cdn.jsdelivr.net/...">` that docsify's own quickstart tells you to write is blocked by the browser in production, while working perfectly in local testing where no headers are applied.

So `scripts/sync-docsify.mjs` copies the runtime out of `node_modules` into `vendor/` during the build. Upgrading is `pnpm up docsify` and a rebuild. Never edit `vendor/` by hand: it is gitignored and regenerated every build.

### The config is a file, not an inline script

Same reason. The CSP has no `unsafe-inline`, and docsify's documented setup puts `window.$docsify` in an inline `<script>` block. It lives in `config.js` instead.

### /docs/ has its own CSP

The one concession. docsify styles inline in two places - the search plugin injects a `<style>` element, and the progress bar animates `style.width` - so `/docs/*` relaxes `style-src` alone. `script-src` stays `'self'`, which is the directive that matters. The rule is at the bottom of `public/_headers`, with the reasoning.

### There is hand-written fallback markup

Every other page on this site is prerendered to real HTML, so a crawler that does not run scripts still sees content. docsify renders in the browser and cannot be prerendered, so `index.html` carries a summary of the docs, written by hand, inside `#root`. docsify replaces it on boot.

`scripts/audit.mjs` loads the page with JavaScript disabled and fails if that fallback drops below 600 characters. If you edit `index.html`, keep it.

## The theme

`theme.css` loads after docsify's stylesheet and restates it in the site's palette: no colour, Instrument Serif headlines, greyscale syntax highlighting.

The palette check (`scripts/palette.mjs`) skips `vendor/` because that file is third-party and full of hue. What makes that skip safe is `scripts/verify-site.mjs`, which loads the rendered docs in a real browser and fails on any computed colour whose channels are not equal. A static scan cannot see a cascade; that check can. If you add a rule that lets docsify's blue through, the browser check is what will catch it.

## Files

| File | What it is |
| --- | --- |
|  The documentation | The documentation |
| `_sidebar.md` | Navigation; every page must be listed |
| `index.html` | The shell, plus the no-JavaScript fallback |
| `config.js` | docsify configuration |
| `theme.css` | The site's palette applied over docsify's |
| `not-found.md` | Shown for an unknown route |
| `vendor/` | Generated at build time, gitignored |
