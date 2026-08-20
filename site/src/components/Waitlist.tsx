import { useState, type FormEvent } from 'react'
import { WAITLIST_ENDPOINT, WAITLIST_FIELD, site } from '../config'

type Status = 'idle' | 'submitting' | 'done' | 'error'

/**
 * Waitlist signup.
 *
 * The form is always visible, because a waitlist you cannot see is not a
 * waitlist. What changes is what happens on submit:
 *
 * - With WAITLIST_ENDPOINT set, the address is POSTed to the provider.
 * - Without it, the submit does not pretend to succeed. It says signups are
 *   not open yet and points at GitHub. Showing a cheerful "you're on the list"
 *   for an address that went nowhere is the one outcome worse than not asking,
 *   because the person believes they will be told and never is.
 *
 * The unconfigured case also warns in the console, so whoever deploys the site
 * finds out during development rather than from a visitor.
 */
export function Waitlist({ id = 'waitlist' }: { id?: string }) {
  const configured = WAITLIST_ENDPOINT.length > 0
  const [status, setStatus] = useState<Status>('idle')
  const [message, setMessage] = useState('')

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = event.currentTarget
    const email = new FormData(form).get(WAITLIST_FIELD)
    if (typeof email !== 'string' || email.length === 0) return

    if (!configured) {
      console.warn(
        '[context0] WAITLIST_ENDPOINT is not set in src/config.ts, so this signup was not stored anywhere.',
      )
      setStatus('error')
      setMessage('Signups are not open yet. Watch the repository and you will not miss the release.')
      return
    }

    setStatus('submitting')
    try {
      const body = new FormData()
      body.append(WAITLIST_FIELD, email)
      const response = await fetch(WAITLIST_ENDPOINT, {
        method: 'POST',
        body,
        headers: { Accept: 'application/json' },
      })
      if (!response.ok) throw new Error(String(response.status))
      setStatus('done')
      form.reset()
    } catch {
      setStatus('error')
      setMessage('That did not go through. Try again, or watch the repository on GitHub.')
    }
  }

  if (status === 'done') {
    return (
      <div
        className="max-w-lg rounded-xl border border-accent/40 bg-accent/[0.06] p-5"
        role="status"
        aria-live="polite"
      >
        <p className="text-sm font-medium text-body">You are on the list.</p>
        <p className="mt-1.5 text-sm text-muted">
          One email when Context0 ships. Nothing else, ever.
        </p>
      </div>
    )
  }

  const inputId = `${id}-email`
  const noteId = `${id}-note`

  return (
    <div className="max-w-lg">
      <form onSubmit={handleSubmit} className="flex flex-col gap-2.5 sm:flex-row">
        <label htmlFor={inputId} className="sr-only">
          Email address
        </label>
        <input
          id={inputId}
          name={WAITLIST_FIELD}
          type="email"
          required
          autoComplete="email"
          placeholder="you@company.com"
          aria-describedby={noteId}
          disabled={status === 'submitting'}
          className="min-h-12 min-w-0 flex-1 rounded-lg border border-line-bright bg-surface px-4 text-sm text-body transition-colors placeholder:text-dim hover:border-line-bright focus:border-brand focus:outline-none disabled:opacity-60"
        />
        <button
          type="submit"
          disabled={status === 'submitting'}
          className="min-h-12 shrink-0 rounded-lg bg-brand px-6 text-sm font-semibold text-white transition-colors hover:bg-brand-deep disabled:opacity-60"
        >
          {status === 'submitting' ? 'Joining...' : 'Join the waitlist'}
        </button>
      </form>

      <p
        id={noteId}
        className={`mt-3 text-[13px] ${status === 'error' ? 'text-red-300/90' : 'text-dim'}`}
        role={status === 'error' ? 'alert' : undefined}
      >
        {status === 'error' ? (
          <>
            {message}{' '}
            <a
              href={site.github}
              target="_blank"
              rel="noopener noreferrer"
              className="underline decoration-red-300/40 underline-offset-4 hover:decoration-red-300"
            >
              Open GitHub
            </a>
          </>
        ) : (
          'One email when it ships. No newsletter, no sharing your address.'
        )}
      </p>
    </div>
  )
}
