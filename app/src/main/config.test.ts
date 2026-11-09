import { describe, expect, it } from 'vitest'
import { apiRequestOrigin } from './config'

describe('apiRequestOrigin', () => {
  const api = 'http://127.0.0.1:8787'

  it('presents the API origin for a file:// page', () => {
    expect(apiRequestOrigin(undefined, api)).toBe(api)
    expect(apiRequestOrigin('null', api)).toBe(api)
    expect(apiRequestOrigin('file://', api)).toBe(api)
  })

  it('keeps the dev server origin', () => {
    expect(apiRequestOrigin('http://127.0.0.1:5173', api)).toBe('http://127.0.0.1:5173')
  })
})
