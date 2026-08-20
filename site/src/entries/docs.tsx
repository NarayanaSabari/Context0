import { StrictMode } from 'react'
import { hydrateRoot } from 'react-dom/client'
import '../index.css'
import { Docs } from '../pages/Docs'

hydrateRoot(
  document.getElementById('root')!,
  <StrictMode>
    <Docs />
  </StrictMode>,
)
