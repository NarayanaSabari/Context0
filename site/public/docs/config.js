// docsify configuration.
//
// In a separate file rather than an inline <script>, which is what docsify's
// quickstart shows: public/_headers sets a Content-Security-Policy with no
// 'unsafe-inline' in script-src, so an inline config block is dropped by the
// browser and docsify never boots - leaving the pre-JavaScript fallback on
// screen, in production only.
//
// Verified rather than assumed: inlining this file under the real CSP gives
// "Executing inline script violates the following Content Security Policy
// directive 'script-src 'self''", no .markdown-section, and zero sidebar links.

window.$docsify = {
  // docsify defaults this to '#app'. The rest of the site renders into '#root',
  // and several of the build checks look there for content, so the docs use the
  // same element rather than being the one page with a different convention.
  el: '#root',

  name: 'kora',
  // Clicking the name goes back to the marketing site rather than to /docs/,
  // which is where the reader already is.
  nameLink: '/',
  repo: 'https://github.com/NarayanaSabari/Kora',

  // docsify fetches Markdown relative to this directory, so the route
  // `#/quickstart` loads /docs/quickstart.md. Without it docsify would look at
  // the site root and every page would 404.
  basePath: '/docs/',
  relativePath: false,

  // Hash routing, which is docsify's default and is kept deliberately.
  //
  // History mode gives prettier URLs (/docs/quickstart instead of
  // /docs/#/quickstart) and in exchange requires the host to rewrite every
  // unmatched path under /docs/ back to this index.html. That rewrite is a
  // piece of hosting configuration with no local equivalent, so a broken one
  // fails only in production, and only on a hard refresh of a deep link -
  // the exact case nobody clicks through before deploying.
  //
  // Measured against a server that mirrors Cloudflare Pages (static files, no
  // SPA rewrite): /docs/#/api cold-loads HTTP 200 and renders "API reference",
  // while history mode's /docs/api returns HTTP 404. Hash routing has no such
  // failure mode - the server only ever serves /docs/index.html, and the
  // fragment never reaches it.
  routerMode: 'hash',

  loadSidebar: true,
  loadNavbar: false,
  subMaxLevel: 2,
  maxLevel: 3,
  auto2top: true,

  // The empty-state page for a URL under /docs/ that does not exist.
  notFoundPage: 'not-found.md',

  // The mark from the site's nav, inlined so the sidebar does not pay for a
  // request. Kept in sync with src/components/Chrome.tsx by hand: it is four
  // rectangles and has not changed since the rebrand.
  logo: false,

  search: {
    noData: 'Nothing matched that.',
    paths: 'auto',
    placeholder: 'Search the docs',
    depth: 3,
  },

  // Coverpage and homepage default to README.md, which is what we want:
  // README.md here is the docs index, not the repository's README.
  homepage: 'README.md',
}
