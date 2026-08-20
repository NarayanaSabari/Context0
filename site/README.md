# The marketing site

The landing page at <https://context0.sabarinarayanakg.in>.

Separate from `web/`, which is the in-cluster graph UI shipped with the product.
This one is a static marketing site: no API calls, no auth, no backend. It
shares the brand mark and color tokens with `web/` so the two read as one
product.

## Pages

| URL | Purpose |
|---|---|
| `/` | Overview and waitlist signup. Deliberately not a product tour: Context0 is pre-release, so the page explains the idea and asks for an email. |
| `/docs/` | The concepts that will not change, plus links into the repository. Real documentation arrives with the first release. |
| `/blog/` | Empty until the first post. Add entries to `POSTS` in `src/pages/Blog.tsx` and the empty state disappears. |
| `/releases/` | Empty until the first release, with the versioning policy written down. |
| `404.html` | Served by Pages for any unmatched path, so a mistyped URL keeps the site's design and offers a way back. |

These are real HTML entry points, not client-side routes. `/blog/` works on
first byte, survives a hard refresh, and needs no 404 redirect hack.

Every page is **prerendered** at build time by `scripts/prerender.mjs`: the same
React components are rendered to static HTML and baked into each file, and the
client hydrates that markup rather than replacing it. Without this the pages
were an empty `<div id="root">` until JavaScript ran, which meant a visitor with
scripts blocked saw a blank screen and a crawler that does not execute
JavaScript indexed nothing but the `<head>`. The home page went from 0 to about
1,900 characters of readable text with JavaScript disabled.

## Local development

```bash
cd site
pnpm install
pnpm dev          # http://localhost:3001
```

Port 3001, so it can run beside the product UI on 3000.

## Wiring up the waitlist

The form is live but does not store anything until you point it somewhere. Set
`WAITLIST_ENDPOINT` in `src/config.ts` to a provider that accepts a plain
browser POST:

```ts
export const WAITLIST_ENDPOINT = 'https://buttondown.com/api/emails/embed-subscribe/<username>'
// or 'https://formspree.io/f/<form-id>'
```

Until it is set, submitting tells the visitor signups are not open yet and
points them at GitHub. It never shows a success message for an address that went
nowhere, because someone who believes they are on a list and is never emailed is
worse off than someone who was told to watch the repo.

That value is public by design - it ships in the bundle. A provider that needs a
secret key is the wrong provider for a static site.

## Before pushing

```bash
pnpm check        # typecheck, build, inspect output, drive it in a browser, audit
pnpm shots        # build and write screenshots to .shots/
```

Three layers, because they catch different things:

- `scripts/check-build.mjs` reads the emitted `dist/`. It catches a nav link to
  a page that was never built, a missing `CNAME` (which would silently drop the
  custom domain on the next deploy), a canonical or `og:url` copy-pasted from
  another page, a relative `og:image`, `target="_blank"` without
  `rel="noreferrer"`, a sitemap that disagrees with the built pages, a page
  missing its prerendered markup, and marketing claims the pre-release project
  cannot back.
- `scripts/verify-site.mjs` serves `dist/` over HTTP and drives it in Chromium
  at 1440px and 390px. It catches what only shows up in a browser: a page that
  renders blank because a component threw, a hydration mismatch, horizontal
  scroll on a phone, tap targets under 24px, duplicate element ids, unlabelled
  inputs, a nav link that 404s, a waitlist that ignores a submit, and a 404 page
  that lost its stylesheet.
- `scripts/audit.mjs` covers what only affects a subset of visitors: pages that
  render nothing with JavaScript disabled, text below the WCAG AA contrast
  ratio, a hero animation that never reaches its end state under
  `prefers-reduced-motion`, interactive elements unreachable by keyboard, and a
  missing custom 404.
- `scripts/cross-browser.mjs` runs the pages in WebKit and Firefox, the two
  engines Chromium testing cannot speak for, and measures layout shift with the
  webfont requests stalled. That check caught the hero jumping 79px when
  Instrument Serif fell back to Georgia; metric-adjusted fallback faces in
  `index.css` brought it to 0.

`VERIFICATION.md` records what each check caught, with before-and-after numbers
and the mutation tests proving each one fails when its fix is reverted.

- `scripts/waitlist.mjs` builds a copy of the site with an endpoint configured,
  points it at a stub, and drives the waitlist against a provider that is slow,
  down, or rejecting. The rule it enforces: never show success for an address
  that did not arrive.

All five run in CI on every pull request that touches `site/`.

```bash
pnpm links        # do the outbound GitHub links still resolve?
```

`scripts/check-links.mjs` is deliberately outside `pnpm check` and CI: it
depends on github.com being reachable, and the build should not go red because
a third party had a bad minute. Run it when the outbound links change.

## The social card

`public/og.png` is generated, not hand-drawn:

```bash
pnpm og
```

It renders the card in the real browser with the site's own fonts, colors, and
logo, so it cannot drift from the design. Re-run it if the wordmark or the
headline changes.

## Deployment

`.github/workflows/site.yaml` builds and publishes to GitHub Pages on every push
to `main` that touches `site/`. Pull requests build and verify but never deploy.

### One-time setup

1. **Enable Pages with the Actions source.** Settings -> Pages -> Source:
   **GitHub Actions**. Or:

   ```bash
   gh api -X POST repos/NarayanaSabari/Context0/pages \
     -f 'build_type=workflow' \
     -f 'source[branch]=main' -f 'source[path]=/'
   ```

2. **Add the DNS record** at the registrar for `sabarinarayanakg.in`:

   | Type | Name | Value | TTL |
   |------|------|-------|-----|
   | CNAME | `context0` | `narayanasabari.github.io.` | 3600 |

   A `CNAME` rather than an `A` record, because this is a subdomain.

   The value is `narayanasabari.github.io`, the *owner's* Pages host, not
   `context0.github.io` and not the project path. The repository is owned by
   the user `NarayanaSabari`, so that is the host every Pages site of theirs is
   served from. Pointing the record at `context0.github.io` would resolve to a
   host that does not serve this site, and the domain would never come up.

3. **Set the custom domain** once DNS resolves:

   ```bash
   gh api -X PUT repos/NarayanaSabari/Context0/pages \
     -f 'cname=context0.sabarinarayanakg.in'
   ```

   Already applied. Note the order: GitHub only issues the TLS certificate
   after the DNS record resolves, and rejects `https_enforced=true` before that
   with "The certificate does not exist yet". Once `dig` returns the record,
   enable it:

   ```bash
   gh api -X PUT repos/NarayanaSabari/Context0/pages \
     -f 'cname=context0.sabarinarayanakg.in' -F 'https_enforced=true'
   ```

   `public/CNAME` is copied into every build so the domain survives redeploys.

4. **Verify:**

   ```bash
   dig +short context0.sabarinarayanakg.in     # expect narayanasabari.github.io
   curl -sI https://context0.sabarinarayanakg.in | head -1
   ```

## Design history

The visual language came out of a four-way parallel design competition: four
agents on four different model families, one shared brief, four complete
independent concepts, compared as real browser renders at both widths. The brief
and the concepts are kept in `.design/` (gitignored).
