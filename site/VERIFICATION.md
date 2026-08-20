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
- **The waitlist round trip.** With no endpoint configured, the submit path is
  verified only as far as the visible error state. The success path is
  unexercised until a provider is wired up.
