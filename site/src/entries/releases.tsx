import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import '../index.css'
import { Releases } from '../pages/Releases'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <Releases />
  </StrictMode>,
)
