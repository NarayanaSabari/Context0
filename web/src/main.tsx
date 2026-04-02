/**
 * @file main.tsx
 * Application entry point for the Context0 Memory Graph web UI.
 * Sets up React strict mode, TanStack Query for data fetching, and mounts
 * the root App component into the DOM.
 */

import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import App from './App'
import './index.css'

/**
 * Global React Query client configured with automatic background refetching.
 * - `refetchInterval`: polls every 30 seconds to keep data fresh.
 * - `staleTime`: treats cached data as fresh for 10 seconds to avoid redundant fetches.
 */
const queryClient = new QueryClient({
  defaultOptions: { queries: { refetchInterval: 30_000, staleTime: 10_000 } },
})

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <App />
    </QueryClientProvider>
  </StrictMode>,
)
