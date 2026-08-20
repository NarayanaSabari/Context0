import { Page } from '../components/Chrome'
import { PageHeader, EmptyState, ButtonLink } from '../components/PageParts'
import { Waitlist } from '../components/Waitlist'
import { site } from '../config'

/**
 * Blog.
 *
 * Empty by design rather than by neglect. Rather than seed it with a
 * placeholder "Hello world" post, the page says what it will carry. When the
 * first real post exists, it goes in POSTS and the empty state disappears on
 * its own.
 */

type Post = {
  title: string
  href: string
  date: string
  summary: string
}

const POSTS: Post[] = []

const TOPICS = [
  {
    title: 'Why a graph, not a pile of chunks',
    body: 'What relationships between memories buy you that similarity search cannot.',
  },
  {
    title: 'Deciding what to forget',
    body: 'Memory that only grows becomes noise. Notes on consolidation and decay.',
  },
  {
    title: 'Building it in the open',
    body: 'Design decisions, the ones that did not work, and what the tests caught.',
  },
]

export function Blog() {
  return (
    <Page current="/blog/">
      <PageHeader
        eyebrow="Blog"
        title="Notes from building Context0."
        intro="Writing about agent memory, the design decisions behind this project, and what breaks along the way. No posts yet - the first ones are being written."
      />

      <section>
        <div className="mx-auto max-w-4xl px-6 py-16">
          {POSTS.length === 0 ? (
            <EmptyState
              title="No posts yet"
              body="The first pieces are in progress. Join the waitlist and they will reach you, or follow the repository where the work happens in public."
              action={
                <>
                  <ButtonLink href={site.github} external>
                    Follow on GitHub
                  </ButtonLink>
                  <ButtonLink href="/docs/" variant="secondary">
                    Read the docs
                  </ButtonLink>
                </>
              }
            />
          ) : (
            <ul className="divide-y divide-line border-y border-line">
              {POSTS.map((post) => (
                <li key={post.href}>
                  <a href={post.href} className="group block py-7">
                    <time className="font-mono text-[11px] uppercase tracking-[0.1em] text-dim">
                      {post.date}
                    </time>
                    <h2 className="mt-2 text-xl font-semibold tracking-tight transition-colors group-hover:text-brand-pale">
                      {post.title}
                    </h2>
                    <p className="mt-2 text-[15px] leading-relaxed text-muted">{post.summary}</p>
                  </a>
                </li>
              ))}
            </ul>
          )}

          <div className="mt-16 border-t border-line pt-12">
            <h2 className="text-lg font-semibold tracking-tight">What is coming</h2>
            <div className="mt-7 grid gap-px overflow-hidden rounded-xl border border-line bg-line sm:grid-cols-3">
              {TOPICS.map((topic) => (
                <article key={topic.title} className="bg-surface p-6">
                  <h3 className="text-[15px] font-semibold leading-snug tracking-tight">
                    {topic.title}
                  </h3>
                  <p className="mt-2.5 text-[14px] leading-relaxed text-muted">{topic.body}</p>
                </article>
              ))}
            </div>
          </div>

          <div className="mt-16 border-t border-line pt-12">
            <h2 className="text-lg font-semibold tracking-tight">Get them by email</h2>
            <p className="mt-3 max-w-xl text-[15px] leading-relaxed text-muted">
              The same list as the waitlist. Nothing more often than it deserves.
            </p>
            <div className="mt-7">
              <Waitlist id="blog" />
            </div>
          </div>
        </div>
      </section>
    </Page>
  )
}
