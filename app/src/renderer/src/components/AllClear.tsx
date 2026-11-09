// AllClear is Needs you at zero: the whole sidebar becomes a photograph (one a
// day, in turn) with the one line that matters: nothing needs you, and how
// many agents are working. Photos are from Wikimedia Commons; the credits the
// licences ask for are in the hover text and in app/CREDITS.md, not on the photo.

import { useEffect } from 'react'
import hohenzollern from '../assets/clear/hohenzollern.jpg'
import matterhorn from '../assets/clear/matterhorn.jpg'
import bled from '../assets/clear/bled.jpg'
import reine from '../assets/clear/reine.jpg'
import moraine from '../assets/clear/moraine.jpg'

// fx, fy: the subject's place in the photo (0..1); w, h: the file's pixels.
export const CLEAR_PHOTOS = [
  { src: hohenzollern, fx: 0.4, fy: 0.42, w: 2600, h: 1733, place: 'Hohenzollern Castle, Germany', credit: 'A. Kniesel, CC BY-SA 3.0' },
  { src: matterhorn, fx: 0.45, fy: 0.35, w: 1610, h: 1073, place: 'Matterhorn, Switzerland', credit: 'chil / Zacharie Gros, CC BY-SA 3.0' },
  { src: bled, fx: 0.53, fy: 0.62, w: 2600, h: 1950, place: 'Lake Bled, Slovenia', credit: 'Canadianhockey91, CC BY-SA 3.0' },
  { src: reine, fx: 0.58, fy: 0.45, w: 2600, h: 1462, place: 'Reine, Lofoten, Norway', credit: 'Christoph Strässler, CC BY-SA 2.0' },
  { src: moraine, fx: 0.55, fy: 0.45, w: 2048, h: 1536, place: 'Moraine Lake, Canada', credit: 'Gorgo, public domain' },
]

type Photo = (typeof CLEAR_PHOTOS)[number]

/**
 * placePhoto sizes the photo to cover a window of W x H and moves it so its
 * subject sits in the middle of the sidebar (nav wide), not behind the main
 * sheet: it scales up just enough that both sides of the subject still cover.
 */
export function placePhoto(p: Photo, W: number, H: number, nav: number) {
  const cx = nav / 2
  const cy = H / 2
  const s = Math.max(W / p.w, H / p.h, cx / (p.fx * p.w), (W - cx) / ((1 - p.fx) * p.w), cy / (p.fy * p.h), (H - cy) / ((1 - p.fy) * p.h))
  const w = p.w * s
  const h = p.h * s
  const x = Math.min(0, Math.max(W - w, cx - p.fx * w))
  const y = Math.min(0, Math.max(H - h, cy - p.fy * h))
  return { size: `${Math.ceil(w)}px ${Math.ceil(h)}px`, at: `${Math.round(x)}px ${Math.round(y)}px` }
}

const DAY = 86_400_000
// The +4 starts the turn on Hohenzollern on 2026-10-30, the day it was asked for.
const today = () => CLEAR_PHOTOS[(Math.floor(Date.now() / DAY) + 4) % CLEAR_PHOTOS.length]

export function AllClear({ agents }: { agents: number }) {
  const photo = today()
  // The photo is the whole window's background: it runs behind the sidebar
  // and around the main sheet, which floats on it.
  useEffect(() => {
    const app = document.querySelector<HTMLElement>('.app')
    if (!app) return
    const place = () => {
      const col = app.querySelector<HTMLElement>('.col-nav')
      // placePhoto centres on half this: the sidebar's middle, past the rail.
      const nav = col ? 2 * col.getBoundingClientRect().left + col.offsetWidth : 300
      const { size, at } = placePhoto(photo, app.offsetWidth, app.offsetHeight, nav)
      app.style.setProperty('--clear-size', size)
      app.style.setProperty('--clear-at', at)
    }
    app.classList.add('is-clear')
    app.style.setProperty('--clear-photo', `url("${photo.src}")`)
    place()
    const ro = new ResizeObserver(place)
    ro.observe(app)
    const nav = app.querySelector('.col-nav')
    if (nav) ro.observe(nav)
    return () => {
      ro.disconnect()
      app.classList.remove('is-clear')
      for (const v of ['--clear-photo', '--clear-size', '--clear-at']) app.style.removeProperty(v)
    }
  }, [photo])
  return (
    <div className="all-clear" role="status" title={`${photo.place}. Photo: ${photo.credit}, via Wikimedia Commons`}>
      <div className="all-clear-words">
        <p className="all-clear-title">All clear.</p>
        <p className="all-clear-sub">
          Nothing needs you.
          {agents > 0 && (
            <>
              <br />
              {agents} agent{agents === 1 ? ' is' : 's are'} on it.
            </>
          )}
        </p>
      </div>
    </div>
  )
}
