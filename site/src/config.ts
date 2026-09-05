/**
 * Site-wide configuration.
 *
 * Everything here is public by definition: this file is bundled into static
 * JavaScript that anyone can read. No secrets, ever. A waitlist provider that
 * needs a secret key is the wrong provider for a static site.
 */

export const site = {
  name: 'kora',
  domain: 'kora.sabarinarayana.com',
  url: 'https://kora.sabarinarayana.com',
  tagline: 'Memory for AI agents',
  description:
    'kora is an open-source memory engine for AI agents. Graph-first, self-hosted, Apache 2.0. v0.1.1 is available.',
  // The repository was renamed to Kora alongside the product. GitHub keeps
  // redirecting the old Context0 URL, but a redirect breaks the moment someone
  // else claims that name, so this points at the real current location.
  github: 'https://github.com/NarayanaSabari/Kora',
} as const

/**
 * Where waitlist signups go.
 *
 * Set this to a form endpoint that accepts a plain POST from the browser -
 * Buttondown, Formspree, Getform and Tally all work this way. Until it is set,
 * the form deliberately refuses to pretend: it tells the visitor signups are
 * not open yet and points them at GitHub, rather than showing a success
 * message for an email that went nowhere. A waitlist that silently discards
 * addresses is worse than no waitlist.
 *
 * Buttondown: https://buttondown.com/api/emails/embed-subscribe/<username>
 * Formspree:  https://formspree.io/f/<form-id>
 */
export const WAITLIST_ENDPOINT = ''

/**
 * The field name the endpoint expects for the email address.
 * Buttondown uses `email`; Formspree also accepts `email`.
 */
export const WAITLIST_FIELD = 'email'

export const nav = [
  { label: 'Docs', href: '/docs/' },
  { label: 'Releases', href: '/releases/' },
] as const
