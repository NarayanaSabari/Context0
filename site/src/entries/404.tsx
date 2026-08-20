import { StrictMode } from 'react'
import { hydrateRoot } from 'react-dom/client'
import '../index.css'
import { NotFound } from '../pages/NotFound'

hydrateRoot(
  document.getElementById('root')!,
  <StrictMode>
    <NotFound />
  </StrictMode>,
)
