import { useEffect, useState } from 'react'

/**
 * The hero demonstration.
 *
 * This is the one piece of the page that shows rather than tells, and it is
 * the distilled version of the strongest idea the design swarm produced: an
 * agent losing its context, then the same exchange with memory intact.
 *
 * It stays at overview level on purpose. No API shapes, no endpoint names, no
 * JSON. The point is the difference between forgetting and remembering, not
 * how the product implements it.
 */

type Mode = 'without' | 'with'

const TRANSCRIPT = [
  { who: 'you', text: 'We moved the project from MySQL to PostgreSQL.' },
  { who: 'agent', text: 'Got it - noted for this project.' },
] as const

export function HeroDemo() {
  const [mode, setMode] = useState<Mode>('without')
  const [faded, setFaded] = useState(false)

  // The fade is the whole point of the "without" state: earlier turns visibly
  // decay, so the empty answer below feels earned rather than asserted. On the
  // dark theme 15% opacity was still legible; on paper that lands almost at the
  // page colour, so the faded state leans on the blur more than the opacity.
  useEffect(() => {
    if (mode !== 'without') {
      setFaded(false)
      return
    }
    const reduced = window.matchMedia('(prefers-reduced-motion: reduce)').matches
    if (reduced) {
      // No animation, but still show the end state: a static render must not
      // leave the visitor looking at a half-told story.
      setFaded(true)
      return
    }
    setFaded(false)
    const timer = window.setTimeout(() => setFaded(true), 1600)
    return () => window.clearTimeout(timer)
  }, [mode])

  const remembering = mode === 'with'

  return (
    <div className="w-full min-w-0">
      {/* Status strip */}
      <div className="mb-2.5 flex items-center justify-between gap-3 font-mono text-[10px] uppercase tracking-[0.12em] text-dim">
        <span>New session, next day</span>
        <span className={`flex items-center gap-2 ${remembering ? 'text-heading' : 'text-dim'}`}>
          <span
            className={`h-1.5 w-1.5 rounded-full ${
              remembering ? 'bg-emphasis' : 'border border-dim bg-transparent'
            }`}
            aria-hidden="true"
          />
          {remembering ? 'Memory on' : 'Memory off'}
        </span>
      </div>

      <div className="overflow-hidden rounded-xl border border-line-bright bg-surface shadow-xl shadow-black/[0.07]">
        {/* Yesterday's conversation */}
        <div className="border-b border-line px-5 py-4">
          <p className="mb-3 font-mono text-[10px] uppercase tracking-[0.1em] text-dim">
            Yesterday
          </p>
          <div className="space-y-2.5">
            {TRANSCRIPT.map((turn) => (
              <div
                key={turn.text}
                className={`grid grid-cols-[3.25rem_minmax(0,1fr)] gap-3 font-mono text-[11px] leading-relaxed transition-all duration-700 ${
                  !remembering && faded ? 'opacity-25 blur-[2px]' : 'opacity-100 blur-0'
                }`}
              >
                <span className={turn.who === 'you' ? 'text-heading' : 'text-dim'}>{turn.who}</span>
                <span className="min-w-0 text-body">{turn.text}</span>
              </div>
            ))}
          </div>
        </div>

        {/* Today's question */}
        <div className="px-5 py-4">
          <div className="mb-3.5 grid grid-cols-[3.25rem_minmax(0,1fr)] gap-3 font-mono text-[11px] leading-relaxed">
            <span className="text-heading">you</span>
            <span className="min-w-0 text-body">Which database does this project use?</span>
          </div>

          {remembering ? (
            <div className="rounded-lg bg-emphasis p-3.5">
              <div className="grid grid-cols-[3.25rem_minmax(0,1fr)] gap-3 font-mono text-[11px] leading-relaxed">
                <span className="text-on-emphasis/70">agent</span>
                <span className="min-w-0 text-on-emphasis">
                  PostgreSQL. It replaced MySQL, and I know which one is current.
                </span>
              </div>
            </div>
          ) : (
            <div className="rounded-lg border border-dashed border-line-bright bg-surface-2/60 p-3.5">
              <div className="grid grid-cols-[3.25rem_minmax(0,1fr)] gap-3 font-mono text-[11px] leading-relaxed">
                <span className="text-dim">agent</span>
                <span className="min-w-0 text-dim">
                  I do not have enough context to answer that.
                </span>
              </div>
            </div>
          )}
        </div>

        {/* Toggle */}
        <button
          type="button"
          onClick={() => setMode(remembering ? 'without' : 'with')}
          aria-pressed={remembering}
          className={`w-full border-t border-line px-5 py-3.5 text-left font-mono text-[11px] font-medium transition-colors ${
            remembering
              ? 'bg-surface-2 text-muted hover:bg-line'
              : 'bg-emphasis text-on-emphasis hover:bg-emphasis-soft'
          }`}
        >
          {remembering ? 'Replay without memory' : 'Replay with kora -->'}
        </button>
      </div>
    </div>
  )
}
