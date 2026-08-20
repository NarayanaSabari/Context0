import type { ReactNode } from 'react'
import { Eyebrow } from './Chrome'

/**
 * Header used by every secondary page, so releases, blog, and docs share one
 * rhythm instead of drifting apart.
 */
export function PageHeader({
  eyebrow,
  title,
  intro,
}: {
  eyebrow: string
  title: string
  intro: ReactNode
}) {
  return (
    <section className="relative overflow-hidden border-b border-line">
      <div className="bg-grid pointer-events-none absolute inset-0" aria-hidden="true" />
      <div className="relative mx-auto max-w-4xl px-6 py-16 sm:py-24">
        <Eyebrow>{eyebrow}</Eyebrow>
        <h1 className="font-display text-[clamp(2.5rem,5.5vw,4rem)] font-normal leading-[1] tracking-[-0.03em]">
          {title}
        </h1>
        <p className="mt-6 max-w-2xl text-[17px] leading-relaxed text-muted">{intro}</p>
      </div>
    </section>
  )
}

/**
 * The empty state.
 *
 * Every one of these pages is empty right now, and pretending otherwise with
 * placeholder posts or fake version numbers would be worse than admitting it.
 * So the empty state is designed: it says plainly that nothing is here yet,
 * explains what will appear, and offers the one action that is actually useful
 * in the meantime.
 */
export function EmptyState({
  title,
  body,
  action,
}: {
  title: string
  body: string
  action: ReactNode
}) {
  return (
    <div className="rounded-xl border border-dashed border-line-bright bg-surface-2/60 px-6 py-14 text-center">
      <div className="mx-auto flex h-11 w-11 items-center justify-center rounded-full border border-line-bright bg-surface">
        <span className="font-mono text-lg text-dim" aria-hidden="true">
          ~
        </span>
      </div>
      <h2 className="mt-5 text-lg font-semibold tracking-tight">{title}</h2>
      <p className="mx-auto mt-2.5 max-w-md text-[15px] leading-relaxed text-muted">{body}</p>
      <div className="mt-7 flex flex-wrap items-center justify-center gap-3">{action}</div>
    </div>
  )
}

export function ButtonLink({
  href,
  children,
  variant = 'primary',
  external = false,
}: {
  href: string
  children: ReactNode
  variant?: 'primary' | 'secondary'
  external?: boolean
}) {
  const base =
    'inline-flex items-center gap-2 rounded-lg px-4 py-2.5 text-sm font-semibold transition-colors'
  const styles =
    variant === 'primary'
      ? 'bg-brand text-white hover:bg-brand-deep'
      : 'border border-line-bright text-body hover:border-brand hover:bg-brand/[0.07] hover:text-brand-ink'
  return (
    <a
      href={href}
      className={`${base} ${styles}`}
      {...(external ? { target: '_blank', rel: 'noopener noreferrer' } : {})}
    >
      {children}
    </a>
  )
}
