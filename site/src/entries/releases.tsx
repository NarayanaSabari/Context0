import { StrictMode } from 'react'
import { hydrateRoot } from 'react-dom/client'
import '../index.css'
import { Releases } from '../pages/Releases'

hydrateRoot(
  document.getElementById('root')!,
  <StrictMode>
    <Releases />
  </StrictMode>,
)
