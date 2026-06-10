/**
 * Purpose: React 18 entry point — mounts the app into #root with BrowserRouter.
 * Inputs: index.html #root div.
 * Outputs: Mounted React tree with routing context.
 * Constraints: StrictMode enabled for development warnings. CSS imported here for Tailwind.
 * SPORT: T-P3-E02-01 scaffold
 */
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import { App } from './App'
import './index.css'

const rootElement = document.getElementById('root')
if (!rootElement) {
  throw new Error('Root element #root not found in document')
}

createRoot(rootElement).render(
  <StrictMode>
    <BrowserRouter>
      <App />
    </BrowserRouter>
  </StrictMode>
)
