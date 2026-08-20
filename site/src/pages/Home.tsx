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
 * commands, no architecture diagram, no endpoint tables. Context0 is not
 * released yet, so the job of this page is to explain the idea clearly enough
 * that the right person joins the waitlist. Depth belongs in the docs once
 * there is something to document.
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
        <div
          className="pointer-events-none absolute -top-40 right-0 h-[30rem] w-[30rem] rounded-full bg-brand/[0.07] blur-[120px]"
          aria-hidden="true"
        />

        <div className="relative mx-auto grid max-w-6xl gap-14 px-6 py-[var(--space-section)] lg:grid-cols-[minmax(0,1fr)_minmax(0,26rem)] lg:items-center lg:gap-20">
          <div className="min-w-0">
            <p className="t-label mb-7 inline-flex items-center gap-2.5 text-brand-ink">
              <span className="h-1.5 w-1.5 rounded-full bg-brand" aria-hidden="true" />
              In development - Apache 2.0
            </p>

            <h1 className="t-display optical-left">
              Your agent
              <br />
              <span className="text-muted">forgets</span> everything.
            </h1>

            <p className="t-lead mt-8 max-w-xl">
              Context0 is an open-source memory engine for AI agents. It remembers what
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
                Context0 records that one memory replaced another, and why. Ask it the same
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
                {/* brand-ink rather than brand: the solid brand tone is tuned
                    for filled buttons, and at this size the darker text tone is
                    the one that clears AA on the white card. */}
                <span className="t-label text-brand-ink">
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
                    <dt className="t-label text-brand-ink">{item.label}</dt>
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
          <div className="reveal relative overflow-hidden rounded-3xl border border-brand/25 bg-gradient-to-b from-brand/[0.07] to-transparent px-7 py-16 sm:px-14">
            <div
              className="pointer-events-none absolute -right-20 -top-24 h-72 w-72 rounded-full bg-brand/[0.10] blur-[100px]"
              aria-hidden="true"
            />
            <div className="relative">
              <h2 className="t-title optical-left max-w-xl">Be there when it ships.</h2>
              <p className="t-lead mt-5 max-w-lg">
                Context0 is being built in the open. Join the waitlist for one email when the
                first release lands, or follow along on{' '}
                <a
                  href={site.github}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="inline-flex min-h-6 items-center text-brand-ink underline decoration-brand/40 underline-offset-4 transition-colors hover:decoration-brand"
                >
                  GitHub
                </a>
                .
              </p>
              <div className="mt-10">
                <Waitlist id="closing" />
              </div>
            </div>
          </div>
        </div>
      </section>
    </Page>
  )
}
