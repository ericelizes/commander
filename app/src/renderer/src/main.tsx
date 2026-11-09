import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { App } from './App'
import './styles.css'
import { demoSelect } from './demo'
import { routeLinksToPanel } from './linkRouting'

demoSelect()

// A live edit can land half-written and crash the app; the fixed version then
// arrives as a hot update with nothing mounted to apply it to. Reload instead
// of staying blank.
if (import.meta.hot) {
  let crashed = false
  window.addEventListener('error', () => (crashed = true))
  import.meta.hot.on('vite:afterUpdate', () => {
    if (crashed) location.reload()
  })
}

routeLinksToPanel()

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
