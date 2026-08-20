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
  // decay, so the empty answer below feels earned rather than asserted.
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
        <span className={`flex items-center gap-2 ${remembering ? 'text-accent' : 'text-muted'}`}>
          <span
            className={`h-1.5 w-1.5 rounded-full ${remembering ? 'bg-accent' : 'bg-muted'}`}
            aria-hidden="true"
          />
          {remembering ? 'Memory on' : 'Memory off'}
        </span>
      </div>

      <div className="overflow-hidden rounded-xl border border-line-bright bg-surface shadow-2xl shadow-black/40">
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
                  !remembering && faded ? 'opacity-15 blur-[2px]' : 'opacity-100 blur-0'
                }`}
              >
                <span className={turn.who === 'you' ? 'text-accent' : 'text-dim'}>{turn.who}</span>
                <span className="min-w-0 text-body/90">{turn.text}</span>
              </div>
            ))}
          </div>
        </div>

        {/* Today's question */}
        <div className="px-5 py-4">
          <div className="mb-3.5 grid grid-cols-[3.25rem_minmax(0,1fr)] gap-3 font-mono text-[11px] leading-relaxed">
            <span className="text-accent">you</span>
            <span className="min-w-0 text-body">Which database does this project use?</span>
          </div>

          {remembering ? (
            <div className="rounded-lg border border-accent/30 bg-accent/[0.06] p-3.5">
              <div className="grid grid-cols-[3.25rem_minmax(0,1fr)] gap-3 font-mono text-[11px] leading-relaxed">
                <span className="text-accent">agent</span>
                <span className="min-w-0 text-body">
                  PostgreSQL. It replaced MySQL, and I know which one is current.
                </span>
              </div>
            </div>
          ) : (
            <div className="rounded-lg border border-red-400/25 bg-red-400/[0.05] p-3.5">
              <div className="grid grid-cols-[3.25rem_minmax(0,1fr)] gap-3 font-mono text-[11px] leading-relaxed">
                <span className="text-red-300/80">agent</span>
                <span className="min-w-0 text-body/80">
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
              ? 'bg-white/[0.03] text-muted hover:bg-white/[0.06]'
              : 'bg-brand/12 text-brand-pale hover:bg-brand/20'
          }`}
        >
          {remembering ? 'Replay without memory' : 'Replay with Context0 -->'}
        </button>
      </div>
    </div>
  )
}
