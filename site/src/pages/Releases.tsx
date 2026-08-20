import { Page } from '../components/Chrome'
import { PageHeader, EmptyState, ButtonLink } from '../components/PageParts'
import { Waitlist } from '../components/Waitlist'
import { site } from '../config'

/**
 * Releases.
 *
 * There is no released version yet, and inventing a "v0.1.0" to fill the page
 * would be a lie with a version number on it. The page explains what will be
 * published here and how releases will be numbered, which is genuinely useful
 * to someone deciding whether to depend on this later.
 */
export function Releases() {
  return (
    <Page current="/releases/">
      <PageHeader
        eyebrow="Releases"
        title="No releases yet."
        intro={
          <>
            Context0 has not cut its first release. When it does, every version will be
            listed here with its changes, and published on{' '}
            <a
              href={`${site.github}/releases`}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex min-h-6 items-center text-brand-pale underline decoration-brand/40 underline-offset-4 transition-colors hover:decoration-brand"
            >
              GitHub Releases
            </a>{' '}
            at the same time.
          </>
        }
      />

      <section>
        <div className="mx-auto max-w-4xl px-6 py-16">
          <EmptyState
            title="Nothing to download yet"
            body="The first release will land when the core is stable enough to be worth your time. Join the waitlist and you will hear about it the day it ships."
            action={
              <>
                <ButtonLink href={site.github} external>
                  Watch the repository
                </ButtonLink>
                <ButtonLink href="/docs/" variant="secondary">
                  Read the docs
                </ButtonLink>
              </>
            }
          />

          <div className="mt-16 border-t border-line pt-12">
            <h2 className="text-lg font-semibold tracking-tight">How versions will work</h2>
            <p className="mt-3 max-w-2xl text-[15px] leading-relaxed text-muted">
              Semantic versioning, with the pre-1.0 caveat that matters: while the major
              version is zero, a minor bump may contain a breaking change. Pin an exact
              version until 1.0, and read the release notes before upgrading.
            </p>

            <dl className="mt-9 grid gap-px overflow-hidden rounded-xl border border-line bg-line sm:grid-cols-3">
              {[
                ['Patch', 'Fixes only. Always safe to take.'],
                ['Minor', 'New capability. Before 1.0, may break something.'],
                ['Major', 'Reserved for 1.0 and the stability promise that comes with it.'],
              ].map(([label, body]) => (
                <div key={label} className="bg-surface p-6">
                  <dt className="font-mono text-[11px] uppercase tracking-[0.1em] text-brand-pale">
                    {label}
                  </dt>
                  <dd className="mt-2.5 text-[14px] leading-relaxed text-muted">{body}</dd>
                </div>
              ))}
            </dl>
          </div>

          <div className="mt-16 border-t border-line pt-12">
            <h2 className="text-lg font-semibold tracking-tight">Know when the first one lands</h2>
            <p className="mt-3 max-w-xl text-[15px] leading-relaxed text-muted">
              One email, on release day.
            </p>
            <div className="mt-7">
              <Waitlist id="releases" />
            </div>
          </div>
        </div>
      </section>
    </Page>
  )
}
