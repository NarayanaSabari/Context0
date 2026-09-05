import { Page } from '../components/Chrome'
import { PageHeader, ButtonLink } from '../components/PageParts'
import { site } from '../config'

/**
 * Releases.
 *
 * This page is maintained deliberately rather than generated from GitHub.
 * It gives a reader the short, product-level release summary; GitHub remains
 * the source for artifacts, checksums, and the complete commit list.
 */
export function Releases() {
  return (
    <Page current="/releases/">
      <PageHeader
        eyebrow="Releases"
        title="v0.1.1 is available."
        intro={
          <>
            The first public kora release packages the engine, CLI, Helm chart,
            container images, Python SDK, and MCP server. Artifacts and checksums are
            published on{' '}
            <a
              href={`${site.github}/releases/tag/v0.1.1`}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex min-h-6 items-center text-heading underline decoration-heading/40 underline-offset-4 transition-colors hover:decoration-heading"
            >
              GitHub Releases
            </a>{' '}
            at the same time.
          </>
        }
      />

      <section>
        <div className="mx-auto max-w-4xl px-6 py-[var(--space-section-tight)]">
          <article className="rounded-2xl border border-line-bright bg-surface-2/60 p-7 sm:p-9">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div>
                <p className="font-mono text-[11px] uppercase tracking-[0.1em] text-dim">
                  Latest release
                </p>
                <h2 className="mt-2 text-2xl font-semibold tracking-[var(--tracking-tight)]">
                  kora v0.1.1
                </h2>
              </div>
              <time className="font-mono text-xs text-dim" dateTime="2026-09-05">
                5 September 2026
              </time>
            </div>

            <p className="mt-6 max-w-2xl text-[15px] leading-relaxed text-muted">
              A self-hosted memory engine for AI agents, with graph and vector retrieval,
              conversation extraction, project profiles, operational tooling, and a
              measurable Razorpay receivables agent.
            </p>

            <ul className="mt-7 grid gap-3 text-[14px] leading-relaxed text-body sm:grid-cols-2">
              {[
                'REST and gRPC APIs with hashed API-key authentication',
                'Hybrid full-text, vector, and graph retrieval',
                'Helm chart, backups, metrics, and production probes',
                'CLI binaries, Python SDK artifact, and MCP server',
                'Docker images for the API, web UI, and PostgreSQL stack',
                'Agent integration and Razorpay example documentation',
              ].map((item) => (
                <li key={item} className="rounded-lg border border-line bg-surface px-4 py-3">
                  {item}
                </li>
              ))}
            </ul>

            <p className="mt-7 max-w-2xl text-[14px] leading-relaxed text-muted">
              This pre-1.0 release includes the Context0 to Kora rename. Existing
              deployments must follow the migration notes before upgrading.
            </p>

            <div className="mt-7 flex flex-wrap gap-3">
              <ButtonLink href={`${site.github}/releases/tag/v0.1.1`} external>
                Download v0.1.1
              </ButtonLink>
              <ButtonLink href="/docs/#/installation" variant="secondary">
                Installation guide
              </ButtonLink>
            </div>
          </article>

          <div className="mt-16 border-t border-line pt-12">
            <h2 className="text-lg font-semibold tracking-[var(--tracking-tight)]">How versions will work</h2>
            <p className="mt-3 max-w-2xl text-[15px] leading-relaxed text-muted">
              Semantic versioning, with the pre-1.0 caveat that matters: while the major
              version is zero, a minor bump may contain a breaking change. Pin an exact
              version until 1.0, and read the release notes before upgrading.
            </p>

            <dl className="mt-9 grid gap-px overflow-hidden rounded-xl border border-line bg-line sm:grid-cols-3">
              {[
                ['Patch', 'Fixes and hardening. Review upgrade notes before 1.0.'],
                ['Minor', 'New capability. Before 1.0, may break something.'],
                ['Major', 'Reserved for 1.0 and the stability promise that comes with it.'],
              ].map(([label, body]) => (
                <div key={label} className="bg-surface p-6">
                  <dt className="font-mono text-[11px] uppercase tracking-[0.1em] text-heading">
                    {label}
                  </dt>
                  <dd className="mt-2.5 text-[14px] leading-relaxed text-muted">{body}</dd>
                </div>
              ))}
            </dl>
          </div>

        </div>
      </section>
    </Page>
  )
}
