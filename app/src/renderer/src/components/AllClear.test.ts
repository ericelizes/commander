import { expect, it } from 'vitest'
import { CLEAR_PHOTOS, placePhoto } from './AllClear'

it('puts the subject in the middle of the sidebar while still covering the window', () => {
  const p = CLEAR_PHOTOS[0]
  const W = 1400, H = 800, nav = 320
  const { size, at } = placePhoto(p, W, H, nav)
  const [w, h] = size.split(' ').map(parseFloat)
  const [x, y] = at.split(' ').map(parseFloat)
  expect(w).toBeGreaterThanOrEqual(W)
  expect(h).toBeGreaterThanOrEqual(H)
  expect(x).toBeLessThanOrEqual(0)
  expect(x + w).toBeGreaterThanOrEqual(W - 1)
  expect(Math.abs(x + p.fx * w - nav / 2)).toBeLessThan(2)
  expect(y).toBeLessThanOrEqual(0)
})
