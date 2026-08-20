import { Page } from '../components/Chrome'
import { PageHeader, ButtonLink } from '../components/PageParts'
import { Waitlist } from '../components/Waitlist'
import { site } from '../config'

/**
 * Docs.
 *
 * A landing page rather than a documentation site. kora is pre-release,
 * and documentation written against an API that has not shipped would be wrong
 * by the time anyone read it. So this explains the concepts, which are stable,
 * and links to the repository for anything that could change.
 *
 * When there is a real release, this page becomes the index for real docs.
 */

const CONCEPTS = [
  {
    term: 'Memory',
    body: 'A single thing worth remembering: a fact, a preference, or something that happened. Extracted from conversation rather than written by hand.',
  },
  {
    term: 'Relationship',
    body: 'A typed link between two memories. One memory can supersede another, be caused by another, or simply relate to it.',
  },
  {
    term: 'Supersede',
    body: 'What happens when new information contradicts old. The old memory is not deleted, it is marked as no longer current, so the history stays queryable.',
  },
  {
    term: 'Profile',
    body: 'The accumulated picture of a user or project: the stable parts, and what has been happening recently.',
  },
]

const LINKS = [
  {
    title: 'Repository',
    body: 'The source, the issues, and the design discussions.',
    href: site.github,
    external: true,
  },
  {
    title: 'Architecture',
    body: 'How the engine is put together, written as the project is built.',
    href: `${site.github}/blob/main/ARCHITECTURE.md`,
    external: true,
  },
  {
    title: 'Contributing',
    body: 'How to set up a development environment and open a pull request.',
    href: `${site.github}/blob/main/CONTRIBUTING.md`,
    external: true,
  },
]

export function Docs() {
  return (
    <Page current="/docs/">
      <PageHeader
        eyebrow="Docs"
        title="Documentation is coming with the first release."
        intro="Writing an API reference before the API is stable produces documentation that is wrong on arrival. Until kora ships, here are the ideas it is built on - those are not going to change - and the source, which is public today."
      />

      <section className="border-b border-line">
        <div className="mx-auto max-w-4xl px-6 py-[var(--space-section-tight)]">
          <h2 className="text-lg font-semibold tracking-[var(--tracking-tight)]">The ideas</h2>
          <p className="mt-3 max-w-2xl text-[15px] leading-relaxed text-muted">
            Four terms cover most of how kora thinks about memory.
          </p>

          <dl className="mt-10 divide-y divide-line border-y border-line">
            {CONCEPTS.map((concept) => (
              <div
                key={concept.term}
                className="grid gap-2 py-6 sm:grid-cols-[minmax(0,10rem)_minmax(0,1fr)] sm:gap-8"
              >
                <dt className="font-mono text-[13px] text-heading">{concept.term}</dt>
                <dd className="text-[15px] leading-relaxed text-muted">{concept.body}</dd>
              </div>
            ))}
          </dl>
        </div>
      </section>

      <section className="border-b border-line">
        <div className="mx-auto max-w-4xl px-6 py-[var(--space-section-tight)]">
          <h2 className="text-lg font-semibold tracking-[var(--tracking-tight)]">Read the source</h2>
          <p className="mt-3 max-w-2xl text-[15px] leading-relaxed text-muted">
            Everything is public while it is being built.
          </p>

          <div className="mt-9 grid gap-px overflow-hidden rounded-xl border border-line bg-line sm:grid-cols-3">
            {LINKS.map((link) => (
              <a
                key={link.title}
                href={link.href}
                target={link.external ? '_blank' : undefined}
                rel={link.external ? 'noopener noreferrer' : undefined}
                className="group bg-surface p-6 transition-colors hover:bg-surface-2"
              >
                <h3 className="flex items-center gap-2 text-[15px] font-semibold tracking-[var(--tracking-tight)] transition-colors group-hover:text-heading">
                  {link.title}
                  <span aria-hidden="true" className="text-dim">
                    -&gt;
                  </span>
                </h3>
                <p className="mt-2.5 text-[14px] leading-relaxed text-muted">{link.body}</p>
              </a>
            ))}
          </div>
        </div>
      </section>

      <section>
        <div className="mx-auto max-w-4xl px-6 py-[var(--space-section-tight)]">
          <h2 className="text-lg font-semibold tracking-[var(--tracking-tight)]">Be told when the docs are real</h2>
          <p className="mt-3 max-w-xl text-[15px] leading-relaxed text-muted">
            The waitlist email goes out the day the first release and its documentation land
            together.
          </p>
          <div className="mt-7">
            <Waitlist id="docs" />
          </div>
          <div className="mt-10">
            <ButtonLink href={site.github} variant="secondary" external>
              Browse the repository
            </ButtonLink>
          </div>
        </div>
      </section>
    </Page>
  )
}
