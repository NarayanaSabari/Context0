import { Home } from '../pages/Home'
import { Releases } from '../pages/Releases'
import { Blog } from '../pages/Blog'
import { Docs } from '../pages/Docs'
import { NotFound } from '../pages/NotFound'

/**
 * Server-side entry, used only at build time by scripts/prerender.mjs.
 *
 * Each page is rendered to static HTML and baked into its index.html, so the
 * site has real content before any JavaScript runs. The client then hydrates
 * the same tree rather than replacing it.
 */
export const pages = {
  main: Home,
  releases: Releases,
  blog: Blog,
  docs: Docs,
  '404': NotFound,
} as const

export type PageName = keyof typeof pages
