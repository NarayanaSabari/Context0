/**
 * @file main.tsx
 * Application entry point for the Kora Memory Graph web UI.
 * Sets up React strict mode and mounts the root App component into the DOM.
 */

import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import App from './App'
import './index.css'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
