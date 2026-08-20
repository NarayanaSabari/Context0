import { Page } from '../components/Chrome'
import { ButtonLink } from '../components/PageParts'
import { Eyebrow } from '../components/Chrome'

/**
 * 404.
 *
 * GitHub Pages serves dist/404.html for any path it cannot match. Without this
 * a mistyped URL lands on GitHub's generic error page, which carries no
 * branding and no way back to the site.
 */
export function NotFound() {
  return (
    <Page>
      <section className="relative overflow-hidden">
        <div className="bg-grid pointer-events-none absolute inset-0" aria-hidden="true" />
        <div className="relative mx-auto max-w-4xl px-6 py-24 sm:py-32">
          <Eyebrow>404</Eyebrow>
          <h1 className="font-display text-[clamp(2.5rem,5.5vw,4rem)] font-normal leading-[1] tracking-[-0.03em]">
            This page does not exist.
          </h1>
          <p className="mt-6 max-w-xl text-[17px] leading-relaxed text-muted">
            The link may be out of date, or the page may not have been written yet. Most of
            this site is still being built.
          </p>
          <div className="mt-9 flex flex-wrap gap-3">
            <ButtonLink href="/">Back to the start</ButtonLink>
            <ButtonLink href="/docs/" variant="secondary">
              Read the docs
            </ButtonLink>
          </div>
        </div>
      </section>
    </Page>
  )
}
