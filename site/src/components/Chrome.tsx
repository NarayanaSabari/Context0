import type { ReactNode } from 'react'
import { site, nav } from '../config'

/**
 * The kora mark: four squares stepping down toward nothing.
 *
 * The shape is the product's own problem statement - context decaying until
 * there is none left - which is why the nav pairs it with the wordmark and the
 * hero does not need to explain it.
 *
 * Each square is ~0.66 of the one before it and they share a common centre
 * line, so the sequence reads as one object receding rather than four separate
 * blocks. Filled with currentColor, so it inherits the ink of whatever it sits
 * in and carries no hue of its own.
 *
 * The favicon is a separate square-format file: this one is 3:1 and would be
 * illegible in a browser tab.
 */
export function Mark({ className = 'h-5' }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 70 24"
      className={`${className} w-auto`}
      fill="currentColor"
      aria-hidden="true"
    >
      <rect x="0" y="0" width="24" height="24" />
      <rect x="28" y="4" width="16" height="16" />
      <rect x="48" y="6.5" width="11" height="11" />
      <rect x="63" y="8.5" width="7" height="7" />
    </svg>
  )
}

function GitHubIcon({ className = 'h-4 w-4' }: { className?: string }) {
  return (
    <svg viewBox="0 0 16 16" className={className} fill="currentColor" aria-hidden="true">
      <path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27s1.36.09 2 .27c1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0 0 16 8c0-4.42-3.58-8-8-8z" />
    </svg>
  )
}

/** Site header. Identical on every page, so the active link is derived from
 *  the URL rather than passed in and forgotten. */
export function Nav({ current }: { current?: string }) {
  return (
    <header className="sticky top-0 z-50 border-b border-line bg-ink/85 backdrop-blur-md">
      <nav
        className="mx-auto flex h-16 max-w-6xl items-center justify-between gap-4 px-6"
        aria-label="Main"
      >
        <a
          href="/"
          className="flex shrink-0 items-center gap-2.5 py-2"
          aria-label={`${site.name} home`}
        >
          <Mark className="h-[13px]" />
          {/* The wordmark is lowercase and tightly tracked, matching the logo
              lockup. It is deliberately not the display serif: the serif is the
              editorial voice for headlines, the wordmark is the brand. */}
          <span className="text-[17px] font-bold lowercase tracking-[-0.03em] text-heading">
            {site.name}
          </span>
        </a>

        <div className="flex items-center gap-1 sm:gap-2">
          {nav.map((item) => {
            const active = current === item.href
            return (
              <a
                key={item.href}
                href={item.href}
                aria-current={active ? 'page' : undefined}
                className={`flex min-h-11 items-center rounded-md px-2.5 text-[13px] transition-colors sm:px-3 ${
                  active ? 'text-heading font-medium' : 'text-muted hover:text-heading'
                }`}
              >
                {item.label}
              </a>
            )
          })}
          {/* On a phone the label is dropped but the link is kept. Hiding it
              entirely, as this did before, left the secondary call to action
              unreachable on the devices where most first visits happen. */}
          <a
            href={site.github}
            target="_blank"
            rel="noopener noreferrer"
            aria-label="kora on GitHub"
            className="ml-1 inline-flex min-h-11 items-center justify-center gap-2 rounded-md border border-line-bright px-2.5 text-[13px] text-body transition-colors hover:border-emphasis hover:text-heading sm:px-3"
          >
            <GitHubIcon />
            <span className="hidden sm:inline">GitHub</span>
          </a>
        </div>
      </nav>
    </header>
  )
}

export function Footer() {
  return (
    <footer className="border-t border-line">
      <div className="mx-auto flex max-w-6xl flex-col gap-6 px-6 py-10 sm:flex-row sm:items-center sm:justify-between">
        <div className="flex items-center gap-2.5 text-sm text-muted">
          <Mark className="h-[10px]" />
          <span>
            <span className="font-bold lowercase tracking-[-0.02em] text-heading">
              {site.name}
            </span>{' '}
            - {site.tagline}
          </span>
        </div>
        <div className="flex flex-wrap items-center gap-x-6 gap-y-1 text-[13px] text-muted">
          <a href="/docs/" className="inline-flex min-h-11 items-center transition-colors hover:text-heading">
            Docs
          </a>
          <a
            href="/releases/"
            className="inline-flex min-h-11 items-center transition-colors hover:text-heading"
          >
            Releases
          </a>
          <a
            href={site.github}
            target="_blank"
            rel="noopener noreferrer"
            className="inline-flex min-h-11 items-center transition-colors hover:text-heading"
          >
            GitHub
          </a>
          <span className="font-mono text-xs text-dim">Apache 2.0</span>
        </div>
      </div>
    </footer>
  )
}

/** Page shell: nav, main landmark, footer. Every page uses it, so the
 *  landmark structure and skip link cannot drift between pages. */
export function Page({ current, children }: { current?: string; children: ReactNode }) {
  return (
    <>
      <a
        href="#main"
        className="sr-only focus:not-sr-only focus:fixed focus:left-4 focus:top-4 focus:z-[60] focus:rounded-md focus:bg-heading focus:px-4 focus:py-2 focus:text-sm focus:font-medium focus:text-ink"
      >
        Skip to content
      </a>
      <Nav current={current} />
      <main id="main">{children}</main>
      <Footer />
    </>
  )
}

/** The small monospace label that opens each section. */
export function Eyebrow({ children }: { children: ReactNode }) {
  return (
    <p className="t-label mb-6 flex items-center gap-3 text-muted">
      <span className="h-px w-8 bg-emphasis" aria-hidden="true" />
      {children}
    </p>
  )
}
