import '@testing-library/jest-dom/vitest'
import { configure } from '@testing-library/react'

// Under load (many agents running tests) the 1s default for findBy/waitFor is too tight.
configure({ asyncUtilTimeout: 5000 })

// jsdom has no layout: the navigator scrolls its cursor row into view.
if (typeof Element !== 'undefined' && !Element.prototype.scrollIntoView) Element.prototype.scrollIntoView = () => {}

// jsdom has no ResizeObserver; assistant-ui's thread viewport observes its content.
if (typeof globalThis.ResizeObserver === 'undefined') {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
}
