import type { ReactNode } from 'react'
import { site, nav } from '../config'

/** The logo mark, inlined from public/favicon.svg so it needs no network
 *  request and inherits currentColor for the glow. */
export function Mark({ className = 'h-6 w-6' }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 48 46"
      className={`${className} glow-brand`}
      fill="none"
      aria-hidden="true"
    >
      <path
        fill="#863bff"
        d="M25.946 44.938c-.664.845-2.021.375-2.021-.698V33.937a2.26 2.26 0 0 0-2.262-2.262H10.287c-.92 0-1.456-1.04-.92-1.788l7.48-10.471c1.07-1.497 0-3.578-1.842-3.578H1.237c-.92 0-1.456-1.04-.92-1.788L10.013.474c.214-.297.556-.474.92-.474h28.894c.92 0 1.456 1.04.92 1.788l-7.48 10.471c-1.07 1.498 0 3.579 1.842 3.579h11.377c.943 0 1.473 1.088.89 1.83L25.947 44.94z"
      />
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
    <header className="sticky top-0 z-50 border-b border-line bg-ink/80 backdrop-blur-md">
      <nav
        className="mx-auto flex h-16 max-w-6xl items-center justify-between gap-4 px-6"
        aria-label="Main"
      >
        <a
          href="/"
          className="flex shrink-0 items-center gap-2.5 py-2 text-[15px] font-semibold tracking-tight"
        >
          <Mark className="h-5 w-5" />
          {site.name}
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
                  active ? 'text-body' : 'text-muted hover:text-body'
                }`}
              >
                {item.label}
              </a>
            )
          })}
          <a
            href={site.github}
            target="_blank"
            rel="noopener noreferrer"
            className="ml-1 hidden min-h-11 items-center gap-2 rounded-md border border-line-bright px-3 text-[13px] text-body transition-colors hover:border-brand hover:bg-brand/10 sm:inline-flex"
          >
            <GitHubIcon />
            GitHub
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
          <Mark className="h-4 w-4" />
          <span>
            {site.name} - {site.tagline}
          </span>
        </div>
        <div className="flex flex-wrap items-center gap-x-6 gap-y-1 text-[13px] text-muted">
          <a href="/docs/" className="inline-flex min-h-11 items-center transition-colors hover:text-body">
            Docs
          </a>
          <a href="/blog/" className="inline-flex min-h-11 items-center transition-colors hover:text-body">
            Blog
          </a>
          <a
            href="/releases/"
            className="inline-flex min-h-11 items-center transition-colors hover:text-body"
          >
            Releases
          </a>
          <a
            href={site.github}
            target="_blank"
            rel="noopener noreferrer"
            className="inline-flex min-h-11 items-center transition-colors hover:text-body"
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
        className="sr-only focus:not-sr-only focus:fixed focus:left-4 focus:top-4 focus:z-[60] focus:rounded-md focus:bg-body focus:px-4 focus:py-2 focus:text-sm focus:font-medium focus:text-ink"
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
    <p className="mb-5 flex items-center gap-3 font-mono text-[11px] font-medium uppercase tracking-[0.14em] text-muted">
      <span className="h-px w-8 bg-brand" aria-hidden="true" />
      {children}
    </p>
  )
}
