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

These are four real HTML entry points, not client-side routes. `/blog/` works on
first byte, survives a hard refresh, and needs no 404 redirect hack on GitHub
Pages.

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
pnpm check        # typecheck, build, inspect output, then drive it in a browser
pnpm shots        # same, and write screenshots to .shots/
```

Two layers, because they catch different things:

- `scripts/check-build.mjs` reads the emitted `dist/`. It catches a nav link to
  a page that was never built, a missing `CNAME` (which would silently drop the
  custom domain on the next deploy), a canonical or `og:url` copy-pasted from
  another page, a relative `og:image`, `target="_blank"` without
  `rel="noreferrer"`, and marketing claims the pre-release project cannot back.
- `scripts/verify-site.mjs` serves `dist/` over HTTP and drives it in Chromium
  at 1440px and 390px. It catches what only shows up in a browser: a page that
  renders blank because a component threw, horizontal scroll on a phone, tap
  targets under 24px, duplicate element ids, unlabelled inputs, a nav link that
  404s, and a waitlist form that does not respond to a submit.

Both run in CI on every pull request that touches `site/`.

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
   gh api -X POST repos/context0/Context0/pages \
     -f 'build_type=workflow' \
     -f 'source[branch]=main' -f 'source[path]=/'
   ```

2. **Add the DNS record** at the registrar for `sabarinarayanakg.in`:

   | Type | Name | Value | TTL |
   |------|------|-------|-----|
   | CNAME | `context0` | `context0.github.io.` | 3600 |

   A `CNAME` rather than an `A` record, because this is a subdomain. It points
   at the org Pages host, not at a project path.

3. **Set the custom domain** once DNS resolves:

   ```bash
   gh api -X PUT repos/context0/Context0/pages \
     -f 'cname=context0.sabarinarayanakg.in' -F 'https_enforced=true'
   ```

   `public/CNAME` is copied into every build so the domain survives redeploys.
   GitHub issues the TLS certificate automatically, usually within fifteen
   minutes of the record resolving.

4. **Verify:**

   ```bash
   dig +short context0.sabarinarayanakg.in
   curl -sI https://context0.sabarinarayanakg.in | head -1
   ```

## Design history

The visual language came out of a four-way parallel design competition: four
agents on four different model families, one shared brief, four complete
independent concepts, compared as real browser renders at both widths. The brief
and the concepts are kept in `.design/` (gitignored).
