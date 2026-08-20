import { useEffect, useState } from 'react'

/**
 * The supersedes demonstration.
 *
 * The design brief calls this "the single strongest idea on the page" and asks
 * for it to be shown rather than asserted.
 *
 * The comparison is deliberately like-for-like: the same question, against the
 * same stored memories, answered two ways. A vector store ranks every chunk
 * that mentions a database by similarity and cannot tell which is current, so
 * the stale MySQL fact scores highest and the answer is wrong. The graph
 * follows a typed edge, finds the newer fact marked as superseding the old,
 * and answers with the reason attached.
 *
 * Nothing here is invented: the query, the facts, and the edge types are the
 * ones from the brief.
 *
 * On correctness without colour: an earlier version encoded right and wrong as
 * cyan and red, which is the encoding that fails for the ~8% of men with a
 * colour vision deficiency and disappears entirely in print. The palette is
 * monochrome now, so the distinction is carried by things that survive both:
 *
 * - The wrong answer is struck through and its row is recessed.
 * - The current fact is a filled black panel; the stale one is outlined grey.
 * - Every state also carries a word - "stale", "current", "supersedes" - so
 *   nothing depends on noticing a visual treatment at all.
 */

type Lane = 'vector' | 'graph'

const CHUNKS = [
  { text: 'We use MySQL for the main datastore.', score: 0.91, stale: true },
  { text: 'MySQL connection pool tuned to 40.', score: 0.88, stale: true },
  { text: 'Moved the project to PostgreSQL.', score: 0.86, stale: false },
]

export function SupersedeDemo() {
  const [lane, setLane] = useState<Lane>('vector')
  const [step, setStep] = useState(0)

  // The graph lane reveals its three parts in sequence: the stale fact, the
  // edge, then the current fact. Sequencing it makes the edge readable as a
  // relationship rather than as one more box on screen.
  useEffect(() => {
    if (lane !== 'graph') {
      setStep(0)
      return
    }
    const reduced = window.matchMedia('(prefers-reduced-motion: reduce)').matches
    if (reduced) {
      // Show the finished state rather than an unplayed one.
      setStep(3)
      return
    }
    setStep(0)
    const timers = [
      window.setTimeout(() => setStep(1), 220),
      window.setTimeout(() => setStep(2), 760),
      window.setTimeout(() => setStep(3), 1260),
    ]
    return () => timers.forEach(window.clearTimeout)
  }, [lane])

  const graph = lane === 'graph'

  return (
    <div className="w-full min-w-0">
      {/* The question both lanes answer. Stated once, above the switch, so it
          is obvious that the only thing changing is how it is answered. */}
      <div className="rounded-t-2xl border border-line-bright bg-surface px-5 py-4 sm:px-7 sm:py-5">
        <p className="t-label text-dim">The question</p>
        <p className="mt-2 font-mono text-[13px] leading-relaxed text-heading sm:text-sm">
          Which database does this project use?
        </p>
      </div>

      {/* Lane switch. The selected lane is a solid black bar rather than a
          tinted one, which is the strongest available "you are here" without
          reaching for a hue. */}
      <div
        className="grid grid-cols-2 border-x border-line-bright"
        role="tablist"
        aria-label="Retrieval strategy"
      >
        {(
          [
            ['vector', 'Vector search'],
            ['graph', 'kora'],
          ] as const
        ).map(([value, label]) => {
          const active = lane === value
          return (
            <button
              key={value}
              type="button"
              role="tab"
              aria-selected={active}
              onClick={() => setLane(value)}
              className={`min-h-11 border-b px-3 py-2.5 text-center text-[13px] transition-colors ${
                active
                  ? 'border-emphasis bg-emphasis font-semibold text-on-emphasis'
                  : 'border-line-bright text-muted hover:bg-surface-2 hover:text-heading'
              }`}
            >
              {label}
            </button>
          )
        })}
      </div>

      <div className="rounded-b-2xl border-x border-b border-line-bright bg-surface px-5 py-6 sm:px-7">
        {graph ? (
          <div>
            <p className="t-label text-dim">Follows the relationship</p>

            <div className="mt-4 space-y-0">
              {/* Stale fact: outlined, recessed, struck through, and labelled. */}
              <div
                className={`rounded-xl border border-line bg-surface-2/70 px-4 py-3 transition-all duration-500 ${
                  step >= 1 ? 'opacity-100 blur-0' : 'opacity-0 blur-[3px]'
                }`}
              >
                <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
                  <span className="t-label rounded bg-line px-1.5 py-0.5 text-dim">Fact</span>
                  <span className="font-mono text-[13px] text-dim line-through decoration-dim/70">
                    uses MySQL
                  </span>
                  <span className="t-label ml-auto text-dim">superseded</span>
                </div>
              </div>

              {/* The typed edge. This is the whole point of the component, so
                  it is the most prominent thing in the lane rather than a thin
                  connector line. */}
              <div
                className={`flex items-center gap-3 py-2.5 pl-4 transition-all duration-500 ${
                  step >= 2 ? 'opacity-100' : 'opacity-0'
                }`}
              >
                <span className="h-8 w-px shrink-0 bg-emphasis/40" aria-hidden="true" />
                <span className="t-label rounded-full border border-emphasis px-2.5 py-1 text-heading">
                  supersedes
                </span>
              </div>

              {/* Current fact: a filled black panel. Inverting it is the
                  monochrome equivalent of the accent treatment it replaced, and
                  it makes the current fact unmistakably the loudest thing in
                  the lane. */}
              <div
                className={`rounded-xl bg-emphasis px-4 py-3 transition-all duration-500 ${
                  step >= 3 ? 'opacity-100 blur-0' : 'opacity-0 blur-[3px]'
                }`}
              >
                <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
                  <span className="t-label rounded bg-white/20 px-1.5 py-0.5 text-on-emphasis">
                    Fact
                  </span>
                  <span className="font-mono text-[13px] font-medium text-on-emphasis">
                    uses PostgreSQL
                  </span>
                  <span className="t-label ml-auto text-on-emphasis/70">current</span>
                </div>
                <p
                  className={`mt-2.5 border-t border-white/20 pt-2.5 font-mono text-[11.5px] leading-relaxed text-on-emphasis/80 transition-opacity duration-500 ${
                    step >= 3 ? 'opacity-100' : 'opacity-0'
                  }`}
                >
                  <span className="text-on-emphasis">because</span> the project needed graph
                  support
                </p>
              </div>
            </div>

            <div
              className={`mt-5 border-t border-line pt-4 transition-opacity duration-500 ${
                step >= 3 ? 'opacity-100' : 'opacity-0'
              }`}
            >
              <p className="t-label text-dim">Answer</p>
              <p className="mt-2 text-[15px] font-medium leading-relaxed text-heading">
                PostgreSQL, and it knows the MySQL fact is no longer current.
              </p>
            </div>
          </div>
        ) : (
          <div>
            <p className="t-label text-dim">Ranks by similarity</p>

            <div className="mt-4 space-y-2">
              {CHUNKS.map((chunk) => (
                <div
                  key={chunk.text}
                  className="flex items-center gap-3 rounded-xl border border-line bg-surface-2/60 px-4 py-3"
                >
                  <span className="font-mono text-[11px] tabular-nums text-dim">
                    {chunk.score.toFixed(2)}
                  </span>
                  <span
                    className={`min-w-0 flex-1 font-mono text-[12.5px] leading-snug ${
                      chunk.stale ? 'text-dim line-through decoration-dim/70' : 'text-muted'
                    }`}
                  >
                    {chunk.text}
                  </span>
                  {chunk.stale ? (
                    <span className="t-label shrink-0 rounded border border-line-bright px-1.5 py-0.5 text-dim">
                      stale
                    </span>
                  ) : null}
                </div>
              ))}
            </div>

            <div className="mt-5 border-t border-line pt-4">
              <p className="t-label text-dim">Answer</p>
              <p className="mt-2 text-[15px] leading-relaxed text-muted">
                <span className="font-medium text-heading line-through decoration-heading/50">
                  MySQL
                </span>
                , because it scored highest. Nothing records that it changed.
              </p>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
