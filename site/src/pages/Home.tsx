import { Page, Eyebrow } from '../components/Chrome'
import { Waitlist } from '../components/Waitlist'
import { HeroDemo } from '../components/HeroDemo'
import { site } from '../config'

/**
 * The home page.
 *
 * Deliberately an overview, not a product tour. No API reference, no install
 * commands, no architecture diagram, no endpoint tables. Context0 is not
 * released yet, so the job of this page is to explain the idea clearly enough
 * that the right person joins the waitlist. Depth belongs in the docs once
 * there is something to document.
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
  return (
    <Page current="/">
      {/* Hero */}
      <section className="relative overflow-hidden border-b border-line">
        <div className="bg-grid pointer-events-none absolute inset-0" aria-hidden="true" />
        <div
          className="pointer-events-none absolute -top-40 right-0 h-[30rem] w-[30rem] rounded-full bg-brand/10 blur-[120px]"
          aria-hidden="true"
        />

        <div className="relative mx-auto grid max-w-6xl gap-14 px-6 py-20 sm:py-28 lg:grid-cols-[minmax(0,1fr)_minmax(0,26rem)] lg:items-center lg:gap-20">
          <div className="min-w-0">
            <p className="mb-6 inline-flex items-center gap-2.5 font-mono text-[11px] font-medium uppercase tracking-[0.12em] text-brand-pale">
              <span
                className="h-1.5 w-1.5 rounded-full bg-brand shadow-[0_0_12px_#863bff]"
                aria-hidden="true"
              />
              In development - Apache 2.0
            </p>

            <h1 className="font-display text-[clamp(3rem,7vw,5.25rem)] font-normal leading-[0.94] tracking-[-0.03em]">
              Your agent
              <br />
              <span className="text-muted">forgets</span> everything.
            </h1>

            <p className="mt-7 max-w-xl text-lg leading-relaxed text-muted">
              Context0 is an open-source memory engine for AI agents. It remembers what
              matters across sessions, understands how those memories relate, and runs
              entirely on your own infrastructure.
            </p>

            <div className="mt-9">
              <Waitlist id="hero" />
            </div>
          </div>

          <div className="min-w-0 lg:pt-6">
            <HeroDemo />
          </div>
        </div>
      </section>

      {/* What it does */}
      <section className="border-b border-line">
        <div className="mx-auto max-w-6xl px-6 py-20 sm:py-24">
          <Eyebrow>What it does</Eyebrow>
          <h2 className="max-w-2xl font-display text-[clamp(2rem,4vw,3.25rem)] font-normal leading-[1.05] tracking-[-0.025em]">
            Memory that knows what changed.
          </h2>
          <p className="mt-5 max-w-2xl text-[17px] leading-relaxed text-muted">
            Most memory for agents is a pile of text chunks retrieved by similarity. That
            finds things that sound alike, but it cannot tell you which fact is still true.
            Context0 keeps the relationships between memories, so it can.
          </p>

          <div className="mt-14 grid gap-px overflow-hidden rounded-xl border border-line bg-line sm:grid-cols-3">
            {CAPABILITIES.map((item, index) => (
              <div key={item.title} className="bg-surface p-7">
                {/* brand-pale rather than brand: #863bff on the panel measures
                    3.7:1, which fails AA at this size. */}
                <span className="font-mono text-[11px] text-brand-pale">
                  {String(index + 1).padStart(2, '0')}
                </span>
                <h3 className="mt-3 text-lg font-semibold tracking-tight">{item.title}</h3>
                <p className="mt-2.5 text-[15px] leading-relaxed text-muted">{item.body}</p>
              </div>
            ))}
          </div>
        </div>
      </section>

      {/* Principles */}
      <section className="border-b border-line">
        <div className="mx-auto max-w-6xl px-6 py-20 sm:py-24">
          <Eyebrow>How it is built</Eyebrow>
          <div className="grid gap-10 lg:grid-cols-[minmax(0,20rem)_minmax(0,1fr)] lg:gap-20">
            <h2 className="font-display text-[clamp(2rem,4vw,3rem)] font-normal leading-[1.05] tracking-[-0.025em]">
              Yours to run.
            </h2>
            <dl className="grid gap-8 sm:grid-cols-3">
              {PRINCIPLES.map((item) => (
                <div key={item.label}>
                  <dt className="font-mono text-[11px] uppercase tracking-[0.1em] text-brand-pale">
                    {item.label}
                  </dt>
                  <dd className="mt-3 text-[15px] leading-relaxed text-muted">{item.body}</dd>
                </div>
              ))}
            </dl>
          </div>
        </div>
      </section>

      {/* Closing call to action */}
      <section>
        <div className="mx-auto max-w-6xl px-6 py-20 sm:py-28">
          <div className="relative overflow-hidden rounded-2xl border border-brand/25 bg-gradient-to-b from-brand/[0.09] to-transparent px-7 py-14 sm:px-14">
            <div
              className="pointer-events-none absolute -right-20 -top-24 h-72 w-72 rounded-full bg-brand/15 blur-[100px]"
              aria-hidden="true"
            />
            <div className="relative">
              <h2 className="max-w-xl font-display text-[clamp(1.9rem,3.6vw,2.85rem)] font-normal leading-[1.08] tracking-[-0.025em]">
                Be there when it ships.
              </h2>
              <p className="mt-4 max-w-lg text-[17px] leading-relaxed text-muted">
                Context0 is being built in the open. Join the waitlist for one email when the
                first release lands, or follow along on {' '}
                <a
                  href={site.github}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="inline-flex min-h-6 items-center text-brand-pale underline decoration-brand/40 underline-offset-4 transition-colors hover:decoration-brand"
                >
                  GitHub
                </a>
                .
              </p>
              <div className="mt-9">
                <Waitlist id="closing" />
              </div>
            </div>
          </div>
        </div>
      </section>
    </Page>
  )
}
