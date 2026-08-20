import { StrictMode } from 'react'
import { hydrateRoot } from 'react-dom/client'
import '../index.css'
import { Blog } from '../pages/Blog'

hydrateRoot(
  document.getElementById('root')!,
  <StrictMode>
    <Blog />
  </StrictMode>,
)
