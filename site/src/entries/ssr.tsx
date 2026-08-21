import { Home } from '../pages/Home'
import { Releases } from '../pages/Releases'
import { Blog } from '../pages/Blog'
import { NotFound } from '../pages/NotFound'

/**
 * Server-side entry, used only at build time by scripts/prerender.mjs.
 *
 * Each page is rendered to static HTML and baked into its index.html, so the
 * site has real content before any JavaScript runs. The client then hydrates
 * the same tree rather than replacing it.
 *
 * /docs/ is not here: it is docsify, served as static files from public/docs/,
 * and it renders Markdown in the browser rather than from a React component.
 * Its equivalent of prerendered content is the fallback markup written by hand
 * inside public/docs/index.html.
 */
export const pages = {
  main: Home,
  releases: Releases,
  blog: Blog,
  '404': NotFound,
} as const

export type PageName = keyof typeof pages
