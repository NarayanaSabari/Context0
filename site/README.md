# The marketing site

The landing page at <https://context0.sabarinarayana.com>.

Separate from `web/`, which is the in-cluster graph UI shipped with the product.
This one is a static marketing site: no API calls, no auth, no backend. It
shares the brand mark and color tokens with `web/` so the two read as one
product.

## Pages

| URL | Purpose |
|---|---|
| `/` | Overview and waitlist signup. Deliberately not a product tour: Kora is pre-release, so the page explains the idea and asks for an email. |
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
pnpm archive      # do the archived design concepts still open?
```

Both sit outside `pnpm check` and CI on purpose. `check-links.mjs` depends on
github.com being reachable, and the build should not go red because a third
party had a bad minute. `check-archive.mjs` guards documentation rather than
the site, so a stale concept file has no business blocking a deploy. Run each
when the thing it covers changes.

## The social card

`public/og.png` is generated, not hand-drawn:

```bash
pnpm og
```

It renders the card in the real browser with the site's own fonts, colors, and
logo, so it cannot drift from the design. Re-run it if the wordmark or the
headline changes.

## Deployment

Hosted on **Cloudflare Pages**. Verified by **GitHub Actions**.

`.github/workflows/site.yaml` runs the five checks on every push and pull
request touching `site/`, and only then uploads the built site to Cloudflare.
Deployment is a push from CI rather than a pull by Cloudflare: the same
artifact that passed the checks is the one that ships, and nothing reaches
production without going through them.

Cloudflare's own git integration is deliberately **not** connected. If it were,
it would build and publish straight from `main` on its own, bypassing every
check here.

### One-time setup

1. **Create the Pages project.** Cloudflare dashboard -> Workers & Pages ->
   Create -> Pages -> **Direct Upload**, named `kora`. Direct Upload rather
   than a git connection, for the reason above. The first real upload comes
   from CI.

2. **Create an API token.** My Profile -> API Tokens -> Create Token, using the
   **Edit Cloudflare Workers** template, or a custom token with:

   | Scope | Permission |
   |---|---|
   | Account -> Cloudflare Pages | Edit |

   Restrict it to the one account. It needs nothing else - not DNS, not zone
   settings, and certainly not account-wide write.

3. **Add both secrets to the repository.** Settings -> Secrets and variables ->
   Actions:

   ```bash
   gh secret set CLOUDFLARE_API_TOKEN --repo NarayanaSabari/Kora
   gh secret set CLOUDFLARE_ACCOUNT_ID --repo NarayanaSabari/Kora
   ```

   The account ID is on the right-hand side of any Cloudflare dashboard page.

4. **Point the custom domain at the Pages project.** Workers & Pages ->
   `kora` -> Custom domains -> Set up a custom domain ->
   `context0.sabarinarayana.com`.

   Cloudflare rewrites the DNS record itself and issues the certificate, so the
   old `CNAME -> narayanasabari.github.io` record should be removed. Unlike
   GitHub Pages, the record here **is** proxied (orange cloud); that is how
   Cloudflare serves and terminates TLS for it.

5. **Verify:**

   ```bash
   curl -sI https://context0.sabarinarayana.com | head -1
   ```

### Rollbacks

Every deploy is retained. Workers & Pages -> `kora` -> Deployments ->
pick a previous one -> Rollback. That is instant and needs no rebuild, which is
the main practical gain over the previous setup.

### What happened to GitHub Pages

It served this site briefly and worked. It was replaced because its TLS
certificate for a custom domain had not issued after roughly fifteen minutes,
while Cloudflare already terminates TLS for this zone and does it in seconds.
`public/CNAME` is now unused by the host but harmless, and `check-build.mjs`
still asserts its contents as a cheap guard against the domain silently
changing in one place and not another.

## Design history

The visual language came out of a four-way parallel design competition: four
agents on four different model families, one shared brief, four complete
independent concepts, compared as real browser renders at both widths.

`design-archive/` keeps the brief and all four concepts, with a write-up of how
they scored and which ideas ended up in the shipped site. Open any of them
directly in a browser; they are self-contained HTML with no build step.
