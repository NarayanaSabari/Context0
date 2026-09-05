import { Page, Eyebrow } from '../components/Chrome'
import { Waitlist } from '../components/Waitlist'
import { HeroDemo } from '../components/HeroDemo'
import { SupersedeDemo } from '../components/SupersedeDemo'
import { useReveal } from '../lib/useReveal'
import { site } from '../config'

/**
 * The home page.
 *
 * Deliberately an overview, not a product tour. No API reference, no install
 * commands, no architecture diagram, no endpoint tables. The job of this page
 * is to explain the idea clearly enough that the right person tries the
 * release. Depth belongs in the docs.
 *
 * The order is an argument, not a list of features:
 *
 * 1. The problem, shown - an agent losing context between sessions.
 * 2. Why the usual fix does not work - the same question answered by
 *    similarity and by relationship, side by side.
 * 3. What that buys you, in three words.
 * 4. Who it is for and on whose terms it runs.
 * 5. The ask.
 */

const CAPABILITIES = [
  {
    title: 'Remember',
    body: 'Feed it a conversation. It pulls out the facts, preferences, and events worth keeping, without being told what to look for.',
  },
  {
    title: 'Connect',
    body: 'Memories are stored as a graph, so a new fact can supersede an old one instead of quietly sitting next to it.',
  },
  {
    title: 'Recall',
    body: 'Ask a question, get the few memories that actually answer it, along with the relationships that explain why.',
  },
]

const PRINCIPLES = [
  {
    label: 'Open source',
    body: 'Apache 2.0, every dependency OSI-approved. No open-core bait, no license rug pull.',
  },
  {
    label: 'Self-hosted',
    body: 'Runs on your own infrastructure. Your conversations never leave it.',
  },
  {
    label: 'Framework-agnostic',
    body: 'Not tied to one model or one agent framework. Any agent can share the same memory.',
  },
]

export function Home() {
  useReveal()

  return (
    <Page current="/">
      {/* Hero */}
      <section className="relative overflow-hidden border-b border-line">
        <div className="bg-grid pointer-events-none absolute inset-0" aria-hidden="true" />

        <div className="relative mx-auto grid max-w-6xl gap-14 px-6 py-[var(--space-section)] lg:grid-cols-[minmax(0,1fr)_minmax(0,26rem)] lg:items-center lg:gap-20">
          <div className="min-w-0">
            <p className="t-label mb-7 inline-flex items-center gap-2.5 text-heading">
              <span className="h-1.5 w-1.5 rounded-full bg-emphasis" aria-hidden="true" />
              v0.1.1 available - Apache 2.0
            </p>

            <h1 className="t-display optical-left">
              Your agent
              <br />
              <span className="text-muted">forgets</span> everything.
            </h1>

            <p className="t-lead mt-8 max-w-xl">
              kora is an open-source memory engine for AI agents. It remembers what
              matters across sessions, understands how those memories relate, and runs
              entirely on your own infrastructure.
            </p>

            <div className="mt-10">
              <Waitlist id="hero" />
            </div>
          </div>

          <div className="min-w-0 lg:pt-6">
            <HeroDemo />
          </div>
        </div>
      </section>

      {/* Why similarity is not enough. The argument of the page, shown rather
          than claimed. */}
      <section className="border-b border-line">
        <div className="mx-auto max-w-6xl px-6 py-[var(--space-section)]">
          <div className="grid gap-14 lg:grid-cols-[minmax(0,1fr)_minmax(0,30rem)] lg:items-center lg:gap-20">
            <div className="reveal min-w-0">
              <Eyebrow>Why a graph</Eyebrow>
              <h2 className="t-title optical-left max-w-[16ch]">
                Similar is not the same as current.
              </h2>
              <p className="t-lead mt-6 max-w-xl">
                Store memories as flat text and retrieval is a popularity contest between
                things that sound alike. The fact you replaced last month still matches the
                question, still scores well, and still comes back as though it were true.
              </p>
              <p className="mt-5 max-w-xl text-[15px] leading-relaxed text-muted">
                kora records that one memory replaced another, and why. Ask it the same
                question and it can follow that edge instead of guessing from wording.
              </p>
            </div>

            <div className="reveal min-w-0">
              <SupersedeDemo />
            </div>
          </div>
        </div>
      </section>

      {/* What it does */}
      <section className="border-b border-line">
        <div className="mx-auto max-w-6xl px-6 py-[var(--space-section)]">
          <div className="reveal">
            <Eyebrow>What it does</Eyebrow>
            <h2 className="t-title optical-left max-w-[18ch]">
              Memory that knows what changed.
            </h2>
            <p className="t-lead mt-6 max-w-2xl">
              Three things, in the order they happen: it reads a conversation, keeps how the
              pieces relate, and hands back only what answers the question.
            </p>
          </div>

          <div className="reveal mt-16 grid gap-px overflow-hidden rounded-2xl border border-line bg-line sm:grid-cols-3">
            {CAPABILITIES.map((item, index) => (
              <div key={item.title} className="bg-surface p-8">
                {/* The step number is set in full ink rather than a muted grey:
                    with no accent colour available, weight and darkness are the
                    only way to make it read as a marker rather than as more
                    body copy. */}
                <span className="t-label text-heading">
                  {String(index + 1).padStart(2, '0')}
                </span>
                <h3 className="mt-4 text-lg font-semibold tracking-[var(--tracking-tight)]">
                  {item.title}
                </h3>
                <p className="mt-3 text-[15px] leading-relaxed text-muted">{item.body}</p>
              </div>
            ))}
          </div>
        </div>
      </section>

      {/* Principles */}
      <section className="border-b border-line">
        <div className="mx-auto max-w-6xl px-6 py-[var(--space-section)]">
          <div className="reveal">
            <Eyebrow>How it is built</Eyebrow>
            <div className="grid gap-12 lg:grid-cols-[minmax(0,20rem)_minmax(0,1fr)] lg:gap-20">
              <h2 className="t-title optical-left">Yours to run.</h2>
              <dl className="grid gap-10 sm:grid-cols-3">
                {PRINCIPLES.map((item) => (
                  <div key={item.label}>
                    <dt className="t-label text-heading">{item.label}</dt>
                    <dd className="mt-3.5 text-[15px] leading-relaxed text-muted">
                      {item.body}
                    </dd>
                  </div>
                ))}
              </dl>
            </div>
          </div>
        </div>
      </section>

      {/* Closing call to action */}
      <section>
        <div className="mx-auto max-w-6xl px-6 py-[var(--space-section)]">
          {/* The closing panel inverts: black card, white text. On a page with
              no accent colour this is the one moment of full contrast, which
              makes the final ask the loudest thing on the page without
              introducing a hue to do it. */}
          <div className="on-emphasis reveal relative overflow-hidden rounded-3xl bg-emphasis px-7 py-16 sm:px-14">
            <div className="relative">
              <h2 className="t-title optical-left max-w-xl text-on-emphasis">
                Follow what ships next.
              </h2>
              <p className="mt-5 max-w-lg text-[length:var(--text-lead)] leading-relaxed text-on-emphasis/75">
                kora is built in the open. Join the list for release updates, or follow
                development on{' '}
                <a
                  href={site.github}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="inline-flex min-h-6 items-center text-on-emphasis underline decoration-white/40 underline-offset-4 transition-colors hover:decoration-white"
                >
                  GitHub
                </a>
                .
              </p>
              <div className="mt-10">
                <Waitlist id="closing" onDark />
              </div>
            </div>
          </div>
        </div>
      </section>
    </Page>
  )
}
