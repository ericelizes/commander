// Pure policy for inspector webviews: what a page may navigate to, which
// popups open as windows, which permissions are granted, which UA we send and
// how we detect a signed-in site. No electron imports: unit-tested in vitest.

import { getDomain } from 'tldts'

// The one persistent partition every inspector webview shares, so sign-ins
// (Notion, Linear, GitHub, Datadog, Slack) survive restarts.
export const PARTITION = 'persist:cockpit'

// Hosts that sign-in flows pass through. Navigation to them is always allowed
// and their popups open as child windows on the same partition.
const AUTH_HOSTS = [
  'accounts.google.com',
  'login.microsoftonline.com',
  'login.live.com',
  'appleid.apple.com',
  'okta.com',
  'oktapreview.com',
  'auth0.com',
  'onelogin.com',
  'duosecurity.com',
  'id.atlassian.com',
  'auth.openai.com',
  'login.microsoft.com',
  'login.windows.net',
  'sts.windows.net',
  'microsoftonline.com',
  'awsapps.com',
  'signin.aws',
  'signin.aws.amazon.com',
  'amazoncognito.com',
  'sso.amazonaws.com',
  'oidc.amazonaws.com',
  'pingidentity.com',
  'pingone.com',
  'jumpcloud.com',
]

// Paths on otherwise-normal sites that are login/OAuth endpoints.
const AUTH_PATHS: Array<[host: string, prefix: string]> = [
  ['www.amazon.com', '/ap/signin'],
  ['github.com', '/login'],
  ['github.com', '/session'],
  ['github.com', '/sessions'],
  ['slack.com', '/oauth'],
  ['slack.com', '/signin'],
  ['www.notion.so', '/login'],
  ['www.notion.com', '/login'],
  ['linear.app', '/login'],
  ['linear.app', '/oauth'],
]

function parse(raw: string): URL | null {
  try {
    return new URL(raw)
  } catch {
    return null
  }
}

function hostMatches(host: string, domain: string): boolean {
  return host === domain || host.endsWith('.' + domain)
}

// Domains one product serves from: Notion's app.notion.com/p/<id> hands the
// page to www.notion.so/<slug> (and back), and a client-side move between them
// must stay in the tab, not open the browser and leave a header-only page.
// Known limit: anyone can publish under notion.site, so from a Notion tab
// evil.notion.site stays in with no question and no session.
const SITE_ALIASES: string[][] = [['notion.so', 'notion.com', 'notion.site']]

// Shared hosting suffixes the public suffix list does not carry (checked against
// the tldts version in package.json). Anyone can take a name under these. Known
// limit: one name under the suffix is the site, so where a platform puts a region
// there (a.usw2.devtunnels.ms) that region's tenants still read as one site. Cost:
// the platform's own site is split too (gitpod.io and api.gitpod.io are neighbours).
// Not atlassian.net or slack.com: postedContentHome compares siteOf on them.
// Known limit: this names only the suffixes we have met. Any other short name under
// a country suffix that neither list carries reads as one site (a.xyz.io and
// evil.xyz.io). The old two-label guess split those but also split real sites
// (hex.pm and sso.hex.pm, www.ibm.io and app.ibm.io), so it stays gone.
const LOCAL_SUFFIXES = [
  'gradio.live',
  'modal.run',
  'app.github.dev',
  'loca.lt',
  'gitpod.io',
  'devtunnels.ms',
  'lhr.life',
  'koyeb.app',
  'railway.app',
  'codesandbox.io',
  'rf.gd',
  'us.to',
  'pgs.sh',
  'ply.gg',
  'tun.ms',
  'lvh.me',
]

// DNS that embeds an IP in the name: nobody owns a name here, and the last label
// is part of an address (10.0.1.5.nip.io, 8.8.1.5.nip.io), so a host under these
// is only itself. A subdomain of one's own host counts as a neighbour too.
const EXACT_HOST_SUFFIXES = ['nip.io', 'xip.io', 'sslip.io']

// registrableDomain is what someone actually owns: the label under the public
// suffix, private (hosting) suffixes included, so a.vercel.app and evil.vercel.app
// differ. A host with none (an IP, a bare suffix, a single label) is only itself.
// The list is the one bundled in tldts, so it ages with that pin. A suffix it does
// not know reads as one site, and that can be worse than the old two-label guess,
// which split some of them by accident (a.xyz.io and b.xyz.io). The longer of the
// list's and the local reading wins: the list knows up.railway.app, under railway.app.
export function registrableDomain(host: string): string {
  const h = host.toLowerCase().replace(/\.$/, '')
  if (EXACT_HOST_SUFFIXES.some((s) => h.endsWith('.' + s))) return h
  const listed = getDomain(h, { allowPrivateDomains: true, extractHostname: false }) ?? h
  const local = LOCAL_SUFFIXES.find((s) => h.endsWith('.' + s))
  if (!local) return listed
  const own = h.split('.').slice(-(local.split('.').length + 1)).join('.')
  return own.length > listed.length ? own : listed
}

// siteOf is what counts as one site: the registrable domain, or the first name of
// a product that serves from several (SITE_ALIASES).
export function siteOf(host: string): string {
  const alias = SITE_ALIASES.find((a) => a.some((d) => hostMatches(host.toLowerCase(), d)))
  return alias ? alias[0] : registrableDomain(host)
}

// questionLead is the name the sign-in question opens with: the part of the host
// its owner cannot choose. That is the registrable domain, except under
// EXACT_HOST_SUFFIXES, where the site is the whole host and anyone gets any labels
// for free (accounts.google.com.8.8.8.8.nip.io), so only the suffix is left. For
// display only: the boundary stays siteOf.
export function questionLead(host: string): string {
  const h = host.toLowerCase().replace(/\.$/, '')
  return EXACT_HOST_SUFFIXES.find((s) => h.endsWith('.' + s)) ?? registrableDomain(h)
}

export function hostOf(raw: string): string {
  return parse(raw)?.hostname ?? ''
}

export function isAuthUrl(raw: string): boolean {
  const u = parse(raw)
  if (!u || u.protocol !== 'https:') return false
  const host = u.hostname.toLowerCase()
  if (AUTH_HOSTS.some((d) => hostMatches(host, d))) return true
  if (/^(oidc|portal\.sso|sso)\.[a-z0-9-]+\.amazonaws\.com$/.test(host)) return true // AWS IAM Identity Center, any region
  return AUTH_PATHS.some(([h, p]) => host === h && (u.pathname === p || u.pathname.startsWith(p + '/')))
}

export type NavDecision = 'allow' | 'external' | 'deny'

// decideNavigation: a tab whose home is tabUrl wants to go to target. Same
// site and sign-in hosts stay in the tab; other web pages open in the default
// browser; mailto opens externally; any other scheme is denied. A sign-in on an
// unlisted provider is a session (decideHop), never a rule of this function.
export function decideNavigation(tabUrl: string, target: string): NavDecision {
  const to = parse(target)
  if (!to) return 'deny'
  if (to.protocol === 'mailto:') return 'external'
  if (to.protocol !== 'http:' && to.protocol !== 'https:') {
    return to.protocol === 'about:' && target === 'about:blank' ? 'allow' : 'deny'
  }
  if (isAuthUrl(target)) return 'allow'
  return homeSite(parse(tabUrl), to) ? 'allow' : 'external'
}

// homeSite is whether to is on the tab's own site (same origin, same host on
// another port, or same registrable domain; loopback hosts only by host).
function homeSite(home: URL | null, to: URL): boolean {
  if (!home || !isWeb(home) || !isWeb(to)) return false
  if (home.origin === to.origin) return true
  if (home.hostname === to.hostname) return true // same host, other port/scheme (localhost dev)
  if (isLoopback(home.hostname) || isLoopback(to.hostname)) return false
  return siteOf(home.hostname) === siteOf(to.hostname)
}

function isWeb(u: URL): boolean {
  return u.protocol === 'http:' || u.protocol === 'https:'
}

function isLoopback(host: string): boolean {
  return host === 'localhost' || host === '127.0.0.1' || host === '[::1]'
}

export type PopupDecision = 'child-window' | 'external' | 'deny'

// decidePopup handles window.open / target=_blank from a page at opener in a
// tab whose home is tabUrl (default: the opener).
// Sign-in popups become child windows (they need the opener and the same
// cookies); everything else goes to the default browser. A popup a login page
// opens on its own site is part of signing in too: Notion's "Continue with
// Google" first opens a notion.so page that then goes to Google, and a popup
// sent to the browser instead reads to Notion as blocked.
// A sign-in window opens no windows of its own (guard.ts): it passes no opener
// and only an 'external' answer does anything, opening the browser. A sign-in on
// an unlisted provider opens through decidePopupHop.
export function decidePopup(target: string, opener = ''): PopupDecision {
  // A login page may open a blank popup and send it somewhere after.
  if (target === '' && opener && onLoginPage(parse(opener) ?? new URL('about:blank'))) return 'child-window'
  const u = parse(target)
  if (!u) return 'deny'
  if (u.protocol === 'about:') return target === 'about:blank' ? 'child-window' : 'deny'
  if (u.protocol === 'mailto:') return 'external'
  if (u.protocol !== 'http:' && u.protocol !== 'https:') return 'deny'
  if (isAuthUrl(target)) return 'child-window'
  const from = parse(opener)
  if (from && onLoginPage(from) && siteOf(from.hostname) === siteOf(u.hostname)) return 'child-window'
  return 'external'
}

// onLoginPage is whether a page is where someone signs in: a known sign-in
// URL, or any site's /login, /signin, /sso, /oauth or /auth page (Notion's
// moved from notion.so to notion.com; we should not have to chase that).
function onLoginPage(u: URL): boolean {
  return isAuthUrl(u.href) || /^\/(login|log-in|signin|sign-in|sso|oauth|auth)(\/|$|\?)/i.test(u.pathname + (u.search ? '?' : ''))
}

// Query keys a sign-in link names the page to come back to with. Keys that
// ordinary links use for other things (origin, target, dest, continue,
// callback, goto) do not count.
const RETURN_KEYS = new Set([
  'redirect', 'redirect_uri', 'redirect_url', 'redirecturi', 'redirecturl', 'redirect_to', 'redirectto',
  'return', 'return_to', 'returnto', 'return_url', 'returnurl', 'returnpath', 'next',
])

// signInFor is whether target is a sign-in for the tab at home, with no
// provider named: an https login page (/login, /signin, /sso, /oauth, /auth)
// on any site whose return key names home's exact origin
// (provider.example/login?redirect=https://box.example/). A hosted app's own
// "Sign in to continue" page sits at an ordinary path and sends Continue to its
// provider's sign-in. A login path alone, or a return key alone, is no
// sign-in: a link's author controls both, and only the pair says the flow
// comes back here. Even the pair only starts a session (startsSignIn).
function signInFor(target: URL, home: URL | null): boolean {
  if (target.protocol !== 'https:' || !onLoginPage(target)) return false
  if (!home || !isWeb(home) || !home.hostname) return false
  for (const [k, v] of target.searchParams) {
    if (!RETURN_KEYS.has(k.toLowerCase())) continue
    const back = parse(v)
    if (back && isWeb(back) && back.origin === home.origin) return true
  }
  return false
}

// Sites where people post links others click (issues, chats, docs): a link
// there naming the page as its return address is anyone's to write, so a tab
// at home on one of them never starts a sign-in session. These products sign
// in through the hosts listed above (AUTH_PATHS, SITES), so nothing is lost.
const POSTED_CONTENT_EXTRA = ['gitlab.com', 'bitbucket.org', 'atlassian.net']

export function postedContentHome(home: string): boolean {
  const u = parse(home)
  if (!u || !isWeb(u)) return false
  const site = siteOf(u.hostname)
  const known = [...AUTH_PATHS.map(([h]) => h), ...SITES.flatMap((x) => [x.cookieDomain].flat()), ...POSTED_CONTENT_EXTRA]
  return known.some((d) => siteOf(d) === site)
}

// A sign-in session: one unlisted provider's sign-in in one tab or one sign-in
// window, started as startSession and ownRedirect say (not always by a click).
// While it runs, every navigation, redirect and popup there stays in the app only
// while it stays on the provider's host (host), a known sign-in host, or comes
// home; a listed host may hand it to another provider (handedOff), anything else
// goes to the browser and ends it. It also ends at home, after SIGN_IN_MAX_MS or
// after SIGN_IN_MAX_HOPS steps. newSession says who may hold one. Its first step
// on the provider needs the user's yes (providerToConfirm): confirmed is that yes.
export type SignIn = { host: string; started: number; hops: number; confirmed?: boolean }

export const SIGN_IN_MAX_MS = 10 * 60_000
export const SIGN_IN_MAX_HOPS = 20
// A navigation or popup within this long of a click or key press in the page
// counts as the user's (Chromium's transient activation lasts as long).
export const GESTURE_MS = 5_000

export type Hop = {
  home: string // the tab's home
  from: string // the page (or redirect hop) being left
  to: string
  signIn: SignIn | null
  gesture: boolean // the user clicked or typed in the page just before
  now: number
  trusted?: (provider: string) => boolean // the user already said yes to this home with provider
}

export type HopResult = { decision: NavDecision; signIn: SignIn | null }

function live(s: SignIn | null, now: number): SignIn | null {
  if (!s) return null
  return now - s.started > SIGN_IN_MAX_MS || s.hops >= SIGN_IN_MAX_HOPS ? null : s
}

// newSession is the session for a step to to, if it may hold one: a session is
// only ever for an https provider off the tab's site and off the sign-in list
// (those stay in without one), on a home where it may start at all.
function newSession(to: URL, home: URL | null, now: number): SignIn | null {
  if (!home || !isWeb(home) || to.protocol !== 'https:' || homeSite(home, to) || isAuthUrl(to.href)) return null
  return postedContentHome(home.href) ? null : { host: to.hostname, started: now, hops: 1 }
}

// providerToConfirm is the provider the user must accept before a step to to
// goes on: the session's own host, until accepted. Any step there is what the
// session alone keeps in the app, so it is where the session starts to count. A
// redirect of our own load only starts the session (ownRedirect), so a short link
// raises no question until its page moves on; a listed host needs no answer.
export function providerToConfirm(to: string, s: SignIn | null): string | null {
  return s && !s.confirmed && parse(to)?.hostname === s.host ? s.host : null
}

// handedOff is the session when a known sign-in host passes the tab to an unlisted
// provider (SAML federation, often a server redirect): no click needed, because
// the question is the gate. Anyone can register a tenant on a listed host, so a
// crafted link gets that question, naming the attacker's host.
function handedOff(h: Hop, to: URL, home: URL | null): SignIn | null {
  const from = parse(h.from)
  return from && isAuthUrl(from.href) ? newSession(to, home, h.now) : null
}

// ownSignInLink: on the tab's own site, a sign-in page that names the tab as its
// way back, where the site is not one on which anyone can post such a link.
function ownSignInLink(to: URL, from: URL | null, home: URL | null): boolean {
  return !!from && !!home && homeSite(home, from) && !postedContentHome(home.href) && signInFor(to, home)
}

// startSession is the one place a step with no session running gets one. Besides
// a hand-off: a step to the tab's own sign-in link, or any step off the tab's
// login page. Those need the user's click, or a yes already given for this home
// with this provider.
function startSession(h: Hop, to: URL, home: URL | null): SignIn | null {
  const handed = handedOff(h, to, home)
  if (handed) return handed
  const from = parse(h.from)
  if (!ownSignInLink(to, from, home) && !(from && homeSite(home, from) && onLoginPage(from))) return null
  const s = newSession(to, home, h.now)
  return s && (h.gesture || h.trusted?.(s.host)) ? s : null
}

// decideHop decides one step of a tab or sign-in window, a navigation or a
// redirect alike, and returns the session that follows it.
export function decideHop(h: Hop): HopResult {
  const to = parse(h.to)
  const home = parse(h.home)
  const s = live(h.signIn, h.now)
  if (s && to && isWeb(to)) {
    if (homeSite(home, to)) return { decision: 'allow', signIn: null }
    if (to.protocol === 'https:' && (to.hostname === s.host || isAuthUrl(to.href))) {
      return { decision: 'allow', signIn: { ...s, hops: s.hops + 1 } }
    }
    const moved = handedOff(h, to, home)
    return moved ? { decision: 'allow', signIn: moved } : { decision: 'external', signIn: null }
  }
  const started = to && startSession(h, to, home)
  if (started) return { decision: 'allow', signIn: started }
  return { decision: decideNavigation(h.home, h.to), signIn: s }
}

// ownRedirect is the session after a redirect of a load we started (a tab's own
// address, a typed URL, a reload). Such a redirect is followed, never judged, so
// the page is in the tab anyway: its host gets a session with no click, or a
// multi-page provider's next step would leave the app. A running session takes
// it as a step; one leading off it moves the session to the new host.
export function ownRedirect(h: Pick<Hop, 'home' | 'to' | 'signIn' | 'now'>): SignIn | null {
  const to = parse(h.to)
  if (!to || !isWeb(to)) return live(h.signIn, h.now)
  const home = parse(h.home)
  if (live(h.signIn, h.now)) {
    const r = decideHop({ ...h, from: '', gesture: false })
    if (r.decision === 'allow') return r.signIn
  }
  return newSession(to, home, h.now)
}

export type PopupResult = { decision: PopupDecision; signIn: SignIn | null }

// decidePopupHop decides a popup from a tab at opener and returns the session
// its child window runs under: a new one by the same rule as a step, the tab's
// own when the tab is signing in, else none. A popup on a running session's
// provider stays in as a step there does.
export function decidePopupHop(h: Hop): PopupResult {
  const to = parse(h.to)
  const home = parse(h.home)
  const s = live(h.signIn, h.now)
  if (s && to?.protocol === 'https:' && to.hostname === s.host) return { decision: 'child-window', signIn: s }
  const started = to && startSession(h, to, home)
  if (started) return { decision: 'child-window', signIn: started }
  if (to && h.gesture && ownSignInLink(to, parse(h.from), home)) return { decision: 'child-window', signIn: null } // on home's site: no session
  const decision = decidePopup(h.to, h.from)
  return { decision, signIn: decision === 'child-window' ? s : null }
}

// Permissions granted to remote pages; everything else is denied.
const ALLOWED_PERMISSIONS = new Set(['clipboard-sanitized-write', 'fullscreen'])

export function permissionAllowed(permission: string): boolean {
  return ALLOWED_PERMISSIONS.has(permission)
}

// chromeUserAgent is a plain Chrome UA for this platform, without "Electron"
// or the app name, so Google/Notion sign-in treat the webview as Chrome.
export function chromeUserAgent(platform: string, chromeVersion: string): string {
  const major = chromeVersion.split('.')[0] || '140'
  const os =
    platform === 'darwin'
      ? 'Macintosh; Intel Mac OS X 10_15_7'
      : platform === 'win32'
        ? 'Windows NT 10.0; Win64; x64'
        : 'X11; Linux x86_64'
  return `Mozilla/5.0 (${os}) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/${major}.0.0.0 Safari/537.36`
}

export type Site = {
  id: 'notion' | 'linear' | 'github' | 'datadog' | 'slack'
  name: string
  loginUrl: string
  cookieDomain: string | string[]
  // A cookie whose name matches means signed in. Best-effort: these are the
  // sites' known session cookies, not a documented contract.
  cookie: RegExp
}

export const SITES: Site[] = [
  { id: 'notion', name: 'Notion', loginUrl: 'https://www.notion.so/login', cookieDomain: ['notion.so', 'notion.com'], cookie: /^token_v2$/ },
  { id: 'linear', name: 'Linear', loginUrl: 'https://linear.app/login', cookieDomain: 'linear.app', cookie: /session|auth|token/i },
  { id: 'github', name: 'GitHub', loginUrl: 'https://github.com/login', cookieDomain: 'github.com', cookie: /^(user_session|logged_in)$/ },
  { id: 'datadog', name: 'Datadog', loginUrl: 'https://app.datadoghq.com/account/login', cookieDomain: 'datadoghq.com', cookie: /^dogweb$/ },
  { id: 'slack', name: 'Slack', loginUrl: 'https://app.slack.com/', cookieDomain: 'slack.com', cookie: /^d$/ },
]

export type CookieLike = { name: string; domain?: string; value?: string; expirationDate?: number }

// signedIn reports per site whether the partition holds its session cookie.
// GitHub's logged_in cookie must say "yes"; expired cookies do not count.
export function signedIn(cookies: CookieLike[], nowSecs = Date.now() / 1000): Record<Site['id'], boolean> {
  const out = {} as Record<Site['id'], boolean>
  for (const site of SITES) {
    out[site.id] = cookies.some((c) => {
      const domain = (c.domain ?? '').replace(/^\./, '').toLowerCase()
      if (![site.cookieDomain].flat().some((d) => hostMatches(domain, d))) return false
      if (!site.cookie.test(c.name) || !c.value) return false
      if (c.expirationDate && c.expirationDate < nowSecs) return false
      return !(c.name === 'logged_in' && c.value !== 'yes')
    })
  }
  return out
}

// MIN_VIEW_PX: below this a Notion-style app lays out into nothing under its
// top bar, so the pane says it is too small instead of showing a blank page.
export const MIN_VIEW_PX = 200

export function viewTooSmall(r: Rect): boolean {
  return r.width < MIN_VIEW_PX || r.height < MIN_VIEW_PX
}

export type Rect = { x: number; y: number; width: number; height: number }

// clampBounds keeps a view's bounds integral, non-negative and inside the
// window's content area (a rect sent mid-resize can overhang it).
export function clampBounds(r: Rect, content: { width: number; height: number }): Rect {
  const x = Math.max(0, Math.min(Math.round(r.x), content.width))
  const y = Math.max(0, Math.min(Math.round(r.y), content.height))
  const width = Math.max(0, Math.min(Math.round(r.width), content.width - x))
  const height = Math.max(0, Math.min(Math.round(r.height), content.height - y))
  return { x, y, width, height }
}

// A renderer macOS reaps under memory pressure is reloaded quietly; one that
// dies again soon after a reload is a crash loop and shows the error. Spaced
// crashes still count toward a cap, so nothing reloads forever.
export const CRASH_LOOP_MS = 10_000
const CRASH_RESET_MS = 60_000
const MAX_AUTO_RELOADS = 3

export type CrashRecovery = { lastReloadAt: number | null; reloads: number }

export function decideCrash(r: CrashRecovery, now: number): 'reload' | 'give-up' {
  if (r.lastReloadAt === null || now - r.lastReloadAt >= CRASH_RESET_MS) return 'reload'
  if (now - r.lastReloadAt < CRASH_LOOP_MS) return 'give-up'
  return r.reloads >= MAX_AUTO_RELOADS ? 'give-up' : 'reload'
}

// noteReload records an automatic reload starting at now.
export function noteReload(r: CrashRecovery, now: number): CrashRecovery {
  const fresh = r.lastReloadAt === null || now - r.lastReloadAt >= CRASH_RESET_MS
  return { lastReloadAt: now, reloads: fresh ? 1 : r.reloads + 1 }
}
