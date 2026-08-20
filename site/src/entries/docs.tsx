import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import '../index.css'
import { Docs } from '../pages/Docs'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <Docs />
  </StrictMode>,
)
