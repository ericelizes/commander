// The navigation guard: wires a tab's or sign-in window's events to the sign-in
// session model (policy.ts). Electron only as types: guard.test.ts drives it
// with a fake emitter.

import type { BrowserWindowConstructorOptions, WebContents } from 'electron'

import { decideHop, decidePopup, decidePopupHop, GESTURE_MS, hostOf, ownRedirect, providerToConfirm, type SignIn } from './policy'

// NavState is what the navigation guard knows of one page (a tab or a sign-in
// window): the sign-in session it runs, the last click or key press, the
// navigation in flight (its latest hop, whether the user started it, whether
// we started it: our own loads and history moves are the user's, so their
// redirects are not judged), and the user's refusals.
export type NavState = { signIn: SignIn | null; gestureAt: number; hop: string; hopGesture: boolean; ours: boolean; refused: Refused }

// Refused is one tab's and all its sign-in windows' record of the user's noes: the
// hosts refused since the user last acted (acted, ours and oursKeepingSession empty
// it for all of them), and when the last no came, which takes back every click made
// before it. A page acting more than 5 seconds after a click, while a No holds, is
// stopped; the way out is a reload or a typed address.
export type Refused = { hosts: string[]; at: number }

export const freshNav = (): NavState => ({ signIn: null, gestureAt: 0, hop: '', hopGesture: false, ours: false, refused: { hosts: [], at: 0 } })

// Input that counts as the user acting in the page.
const GESTURES = new Set(['mouseDown', 'mouseUp', 'rawKeyDown', 'keyDown', 'gestureTap', 'touchEnd'])

// ours marks the next navigation of a tab as started by us (a load or history
// move the user asked for): it ends any sign-in session and the user's refusals,
// and its redirects are not judged.
export function ours(nav: NavState): void {
  oursKeepingSession(nav)
  nav.signIn = null
}

// oursKeepingSession is ours for the user's reload: the same page again, so a
// sign-in running on it goes on.
export function oursKeepingSession(nav: NavState): void {
  recovering(nav)
  nav.refused.hosts.length = 0
}

// recovering is a reload after a crash, which the page can cause: the same page
// again, and its refusals stand, so while they do its redirects are judged.
export function recovering(nav: NavState): void {
  nav.ours = true
}

function gestured(nav: NavState): boolean {
  return Date.now() - nav.gestureAt < GESTURE_MS && nav.gestureAt > nav.refused.at
}

// acted is whether the user just acted in this page: a click or key in it under 5
// seconds old. That ends the refusals, so the step is judged afresh. A page acting
// more than 5 seconds after a click, while a No holds, is stopped; the way out is a
// reload or a typed address.
function acted(nav: NavState): boolean {
  if (!gestured(nav)) return false
  nav.refused.hosts.length = 0
  return true
}

type OpenExternal = (url: string) => void

// busy: another question is open, so this one was not asked.
export type Answer = 'yes' | 'no' | 'busy'

// ProviderGate says whether home's tab may sign in through an unlisted provider.
// ask answers at once: window.open needs its verdict before it returns, and a
// stopped form post could not be sent again. asker is the page that wants to go.
export type ProviderGate = {
  trusted: (provider: string, home: string) => boolean
  ask: (provider: string, home: string, asker: WebContents) => Answer
}

// question is the gate's answer about the session's provider, 'yes' when there
// is nothing to ask. After a no nothing more is asked until the user acts,
// whichever host is named next; a question that throws is a no.
function question(provider: string, home: string, nav: NavState, gate: ProviderGate, asker: WebContents): Answer {
  try {
    if (gate.trusted(provider, home)) return 'yes'
    return nav.refused.hosts.length ? 'busy' : gate.ask(provider, home, asker)
  } catch (err) {
    console.warn(`sign-in question failed: ${String(err)}`)
    return 'no'
  }
}

// settle turns the policy's verdict on a step to url into what happens. Until the
// user acts after a no (acted, so whatever gets here is the page alone), a step
// out of the app or to a refused host is stopped with no question and no browser.
// A no opens the browser once, ends the session and takes back the click, so the
// page cannot use it for its next try.
function settle(url: string, inApp: boolean, external: boolean, s: SignIn | null, home: string, nav: NavState, gate: ProviderGate, asker: WebContents): { go: boolean; open: boolean; signIn: SignIn | null } {
  const host = hostOf(url)
  const held = nav.refused.hosts.length > 0
  if (nav.refused.hosts.includes(host)) return { go: false, open: false, signIn: null }
  if (!inApp) return { go: false, open: external && !held, signIn: s }
  const provider = providerToConfirm(url, s)
  if (!s || !provider) return { go: true, open: false, signIn: s }
  const answer = question(provider, home, nav, gate, asker)
  if (answer === 'yes') return { go: true, open: false, signIn: { ...s, confirmed: true } }
  if (answer === 'no') {
    nav.refused.hosts.push(host)
    nav.refused.at = Date.now()
    nav.signIn = null
    nav.hopGesture = false
  }
  return { go: false, open: !held, signIn: null }
}

// guard judges every main-frame navigation and redirect of wc (a tab or a
// sign-in window) with decideHop and keeps its session in nav. A step it does
// not allow is stopped; an external one opens in the browser. A redirect of our
// own load is followed while nothing is held, and may start a session (ownRedirect).
// A page moving by itself ends no refusal.
function guard(wc: WebContents, home: () => string, nav: NavState, openExternal: OpenExternal, gate: ProviderGate): void {
  wc.on('input-event', (_ev, input) => {
    if (GESTURES.has(input.type)) nav.gestureAt = Date.now()
  })
  const step = (ev: { url: string; preventDefault: () => void }, from: string, gesture: boolean): void => {
    const trusted = (provider: string) => gate.trusted(provider, home())
    const r = decideHop({ home: home(), from, to: ev.url, signIn: nav.signIn, gesture, now: Date.now(), trusted })
    const o = settle(ev.url, r.decision === 'allow', r.decision === 'external', r.signIn, home(), nav, gate, wc)
    nav.signIn = o.signIn
    if (o.go) {
      nav.hop = ev.url
      return
    }
    nav.hop = ''
    ev.preventDefault()
    if (o.open) openExternal(ev.url)
  }
  wc.on('will-navigate', (ev) => {
    nav.ours = false
    nav.hopGesture = acted(nav)
    step(ev, wc.getURL(), nav.hopGesture)
  })
  wc.on('will-redirect', (ev) => {
    if (!ev.isMainFrame) return
    if (nav.ours && !nav.refused.hosts.length) nav.signIn = ownRedirect({ home: home(), to: ev.url, signIn: nav.signIn, now: Date.now() })
    else step(ev, nav.hop || wc.getURL(), nav.hopGesture)
  })
  wc.on('did-navigate', () => {
    nav.ours = false
    nav.hop = ''
  })
}

// guardTab is guard for a tab plus its popups: window.open goes through
// decidePopupHop, and a sign-in window it allows runs under the same guard with
// the session and the click it was opened with, and the tab's refusals. That
// window may not spawn further windows. While a no holds, a popup with no click is
// denied: a key landing in such a window would end the no.
export function guardTab(
  wc: WebContents,
  home: () => string,
  nav: NavState,
  openExternal: OpenExternal,
  windowOptions: () => BrowserWindowConstructorOptions,
  gate: ProviderGate,
): void {
  guard(wc, home, nav, openExternal, gate)
  let opening: Pick<NavState, 'signIn' | 'hopGesture'> | null = null
  wc.setWindowOpenHandler(({ url }) => {
    const gesture = acted(nav)
    if (!gesture && nav.refused.hosts.length) return { action: 'deny' }
    const trusted = (provider: string) => gate.trusted(provider, home())
    const r = decidePopupHop({ home: home(), from: wc.getURL(), to: url, signIn: nav.signIn, gesture, now: Date.now(), trusted })
    const o = settle(url, r.decision === 'child-window', r.decision === 'external', r.signIn, home(), nav, gate, wc)
    if (o.go) {
      opening = { signIn: o.signIn, hopGesture: gesture }
      return { action: 'allow', overrideBrowserWindowOptions: windowOptions() }
    }
    if (o.open) openExternal(url)
    return { action: 'deny' }
  })
  wc.on('did-create-window', (child, details) => {
    const childNav: NavState = { ...freshNav(), ...opening, gestureAt: nav.gestureAt, hop: details.url, refused: nav.refused }
    opening = null
    guard(child.webContents, home, childNav, openExternal, gate)
    child.webContents.setWindowOpenHandler(({ url }) => {
      acted(childNav)
      if (decidePopup(url) === 'external' && !childNav.refused.hosts.length) openExternal(url)
      return { action: 'deny' }
    })
  })
}
