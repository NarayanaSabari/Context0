# What the checks actually caught

Every claim in this file was produced by running the current checkers against a
build that predates the fix, and by reverting each fix one at a time to confirm
the check fails when the code is wrong. A check that never fails is not
evidence, it is decoration.

Reproduce any row by checking out the "before" commit into a scratch worktree,
building it, and running the current script against that `dist/`.

## Before and after

| Defect | Before | After | Caught by |
|---|---|---|---|
| Pages render nothing without JavaScript | 0 chars of text on all 4 pages | 1,928 chars on `/` | `audit.mjs` |
| Body text below WCAG AA contrast | 3.36:1 and 3.48:1 at 10-13px | all text at or above 4.5:1 | `audit.mjs` |
| Small brand numerals below AA | 3.71:1 | uses `brand-pale` | `audit.mjs` |
| No custom 404 | GitHub's generic error page | branded page with a way back | `audit.mjs`, `verify-site.mjs` |
| Hero jumps when the webfont lands | 237px -> 158px, a 79px shift | 158px -> 158px, 0px | `cross-browser.mjs` |

All three engines a visitor actually arrives in are exercised: Chromium in
`verify-site.mjs`, WebKit and Firefox in `cross-browser.mjs`. Neither WebKit nor
Firefox needed a code change; both render the pages identically to Chromium,
including the `mask-image` grid backdrop.
| Nav and footer tap targets | 20-23px tall | 44px minimum | `verify-site.mjs` |

Running the current `audit.mjs` against the pre-fix build (`b628afb`) reports
**19 findings**. Against the current build it reports **0**.

## Mutation testing

Reverting a fix must make its check fail. Each of these was applied to the
current tree, built, and checked:

| Reverted change | Result |
|---|---|
| `--color-dim` back to `#65657a` | `audit.mjs`: 13 contrast findings |
| Drop `Instrument Serif Fallback` from the display stack | `cross-browser.mjs`: hero shifts 79px |
| Remove the prerender step from `build` | `check-build.mjs`: 10 failures; `audit.mjs`: 4 high findings |

The prerender case is caught independently by two layers, which is the intent:
`check-build.mjs` sees the empty `#root` in the emitted HTML, and `audit.mjs`
sees the consequence in a real browser with scripts disabled.

## Integration boundaries

Claims about how the site connects to things outside itself, each checked
rather than assumed:

| Claim | How it was verified |
|---|---|
| Design tokens match the product UI | `#0a0a0f` and `#e1e2e8` in `site/src/index.css` are byte-identical to `web/src/index.css` |
| The logo is the real mark | The SVG path in `Chrome.tsx` compares equal to the one in `web/public/favicon.svg` |
| Brand colours come from the logo | `#863bff`, `#7e14ff`, `#ede6ff`, `#47bfff` each appear in `favicon.svg` |
| Outbound links resolve | All four GitHub URLs return 200, including the deep links to `ARCHITECTURE.md` and `CONTRIBUTING.md` |
| No unexpected third parties | The built HTML references only the site origin, github.com, and Google Fonts |
| The deploy artifact is valid | A manual workflow run produced a 193 KB `github-pages` artifact; the upload step succeeded |

The origin allowlist was mutation-tested: injecting
`https://cdn.evil-analytics.example.com` into a page makes `check-build.mjs`
fail with `unexpected third-party origin`.

`check-links.mjs` is deliberately **not** in `pnpm check` or CI. It depends on
github.com being reachable, and a marketing site's build should not go red
because a third party had a bad minute. Run it manually when the outbound links
change.

## The waitlist, off the happy path

The waitlist is the only interactive thing on the site and the only reason it
exists, so its failure modes are tested rather than assumed. `waitlist.mjs`
builds a second copy of the site with an endpoint configured (Vite inlines the
constant, so patching the bundle would be brittle), points it at a stub it
controls, and drives it:

| Condition | Required behaviour |
|---|---|
| Provider returns 500 | Shows an error, never "you are on the list" |
| Network drops | Shows an error, never a success |
| Provider is slow | Button reads "Joining...", button and input both disabled |
| Slow request then succeeds | Confirmation appears |
| Address is `not-an-email` | Zero requests reach the provider |
| Valid submission | Request carries an `email` field with the typed address |
| No endpoint configured | Says signups are not open, sends nothing, offers GitHub |

Mutation-tested by deleting the `if (!response.ok) throw` line - the exact bug
the test exists to prevent. Two checks fail immediately: *"a 500 from the
provider still showed a success message"*.

This is also the first exercise of the configured success path, which until now
was listed as uncovered.

## The design archive

`design-archive/README.md` tells the reader to open any concept directly in a
browser. `check-archive.mjs` holds that promise: it opens each of the four from
its archived location over `file://`, scrolls the full page, and fails on a
thrown exception, missing `<h1>`, absent SVG, or a truncated page. It also
cross-checks that the README's table and the files on disk agree in both
directions.

Getting this check to be *real* took three attempts, which is the useful part:

1. The first version loaded each page and waited. It passed on a copy of
   concept A with its original canvas bug deliberately reintroduced, because
   the graph only draws once its section scrolls into view.
2. Adding a scroll pass caught it - then stopped catching it after I scrolled
   back to the top, because the animation loop stops running the affected
   branch once the section leaves the viewport.
3. The version that works scrolls in half-viewport steps, waits on
   `requestAnimationFrame` rather than a bare timeout, and stays at the bottom.

Mutation-tested: with the bug reintroduced it reports
*"concept-a.html throws: Failed to execute 'addColorStop' on 'CanvasGradient'"*,
and passes cleanly once restored.

## Bugs found in the checkers themselves

Worth recording, because a checker that reports a false pass is worse than no
checker at all:

- The keyboard check compared focused elements by text label, so "GitHub"
  appearing four times counted once. It reported 9 of 16 elements reachable
  when tabbing by hand reached all 16 in the correct order. It compares element
  identity now.
- The prerender detector's regex stopped at the first nested `</div>` rather
  than the matching one, so it reported 0 characters of markup for a fully
  prerendered page.
- The screenshot harness captured the above-the-fold image after a full-page
  pass had already scrolled the page, producing a blank hero for one concept.
- A screenshot call waited on `document.fonts.ready` while the test was
  deliberately blocking font requests, so it hung until timeout.

## Not covered

Honest gaps, so nobody reads more into the green checkmarks than is there:

- **Real devices.** Viewport emulation is not a phone. Touch behaviour, iOS
  Safari's dynamic toolbar, and real network latency are unverified.
- **The live domain.** Everything is measured against a local server serving
  `dist/`. Until the DNS record exists, nothing has been checked against
  `context0.sabarinarayanakg.in` itself.
- **The publish step.** `actions/upload-pages-artifact` is proven to produce a
  valid artifact, but `actions/deploy-pages` has never run: it is gated on
  `main`, and this work is still on a branch. The repository has zero Pages
  deployments so far. The first merge to `main` is the first real test of that
  step.
- **A real provider.** The waitlist is driven against a stub, which proves the
  page's half of the contract: what it sends, and how it behaves when the
  answer is slow, absent, or an error. Whether Buttondown or Formspree accepts
  that exact payload is unverified until one is configured.
