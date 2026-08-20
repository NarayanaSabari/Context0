import { StrictMode } from 'react'
import { hydrateRoot } from 'react-dom/client'
import '../index.css'
import { Home } from '../pages/Home'

// hydrateRoot, not createRoot: the markup is prerendered at build time by
// scripts/prerender.mjs, so React adopts the existing DOM instead of throwing
// it away and rebuilding it.
hydrateRoot(
  document.getElementById('root')!,
  <StrictMode>
    <Home />
  </StrictMode>,
)
