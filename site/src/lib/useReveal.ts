import { useEffect } from 'react'

/**
 * Reveals elements marked with `.reveal` as they enter the viewport.
 *
 * Deliberately one effect for the whole page rather than a component per
 * section: a single observer is cheaper, and more importantly it means the
 * timing is defined in one place instead of drifting between sections.
 *
 * The dangerous part of any reveal-on-scroll is that it hides content by
 * default. Anything that prevents the reveal from firing does not degrade to a
 * missing animation, it degrades to a blank page, so every path that can leave
 * an element hidden is closed here:
 *
 * - JavaScript never runs, or fails. The hidden state lives under `.js-reveal`
 *   on the document element, which only this hook adds. No hook, no hiding.
 * - No IntersectionObserver. Bails out before adding the class.
 * - Reduced motion. Same: bails out before adding the class.
 * - The element never intersects. This is the one that bit a full-page
 *   screenshot, where the viewport never moves and offscreen sections stayed
 *   invisible. The safety timeout below reveals anything still hidden, so the
 *   worst case is an un-animated element rather than a missing one.
 * - Printing. Handled in CSS, since a print run has no scrolling at all.
 */
export function useReveal() {
  useEffect(() => {
    const targets = Array.from(document.querySelectorAll<HTMLElement>('.reveal'))
    if (targets.length === 0) return

    const reduced = window.matchMedia('(prefers-reduced-motion: reduce)').matches
    if (reduced || typeof IntersectionObserver === 'undefined') {
      // Nothing to animate, and the class is never added, so the CSS keeps
      // everything visible.
      return
    }

    const root = document.documentElement
    root.classList.add('js-reveal')

    const show = (el: Element) => el.classList.add('is-in')

    const observer = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          if (!entry.isIntersecting) continue
          show(entry.target)
          // Once revealed, stop watching. This is a one-shot reveal, not a
          // scroll-linked effect that replays on the way back up.
          observer.unobserve(entry.target)
        }
      },
      // A small negative bottom margin means the reveal starts once the
      // element is meaningfully on screen rather than the instant its first
      // pixel appears.
      { rootMargin: '0px 0px -8% 0px', threshold: 0.08 },
    )

    for (const target of targets) observer.observe(target)

    // The safety net. Long enough that normal scrolling animates as intended,
    // short enough that nobody is left looking at a gap.
    const failsafe = window.setTimeout(() => {
      for (const target of targets) show(target)
      observer.disconnect()
    }, 2000)

    return () => {
      window.clearTimeout(failsafe)
      observer.disconnect()
      root.classList.remove('js-reveal')
    }
  }, [])
}
