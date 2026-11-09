import { EventEmitter } from 'node:events'

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { freshNav, guardTab, ours, oursKeepingSession, recovering, type Answer, type NavState, type ProviderGate } from './guard'
import { GESTURE_MS } from './policy'

// FakePage stands in for a WebContents: emit its events, read what the guard did.
class FakePage extends EventEmitter {
  url = ''
  openHandler: ((d: { url: string }) => { action: string; overrideBrowserWindowOptions?: unknown }) | null = null
  getURL = (): string => this.url
  setWindowOpenHandler = (h: FakePage['openHandler']): void => {
    this.openHandler = h
  }
  click(): void {
    this.emit('input-event', {}, { type: 'mouseDown' })
  }
  // fire emits a navigation event and returns whether the guard stopped it.
  fire(event: 'will-navigate' | 'will-redirect', url: string, isMainFrame = true): boolean {
    let stopped = false
    this.emit(event, { url, isMainFrame, preventDefault: () => (stopped = true) })
    return stopped
  }
  // arrive is a navigation finishing: the page is now at url.
  arrive(url: string): void {
    this.url = url
    this.emit('did-navigate')
  }
}

const home = 'https://box-1.term.example/'
const signIn = 'https://provider.example/login?redirect=' + encodeURIComponent('https://box-1.term.example/shell')
const verify = 'https://provider.example/login/verify'

// fakeGate records every question and who asked it; trusted names providers
// already accepted for the home at, so a guard passing the wrong home is not trusted.
function fakeGate(asked: string[], answer: boolean | Answer | (() => Answer), trusted: string[] = [], at = home, askers: unknown[] = []): ProviderGate {
  return {
    trusted: (provider, from) => from === at && trusted.includes(provider),
    ask: (provider, from, asker) => {
      asked.push(`${provider} from ${from}`)
      askers.push(asker)
      if (typeof answer === 'function') return answer()
      return typeof answer === 'boolean' ? (answer ? 'yes' : 'no') : answer
    },
  }
}

function setup(nav: NavState = freshNav(), answer: boolean | Answer | (() => Answer) = true, trusted: string[] = [], at = home) {
  const page = new FakePage()
  const opened: string[] = []
  const asked: string[] = []
  const askers: unknown[] = []
  page.url = at
  guardTab(page as never, () => at, nav, (u) => opened.push(u), () => ({ width: 520 }), fakeGate(asked, answer, trusted, at, askers))
  return { page, nav, opened, asked, askers }
}

// open is a window.open from page: a child window comes up if the guard allows it.
function open(page: FakePage, url: string) {
  const verdict = page.openHandler!({ url })
  const child = new FakePage()
  if (verdict.action === 'allow') {
    child.url = url
    page.emit('did-create-window', { webContents: child }, { url })
  }
  return { verdict, child }
}

describe('guard wiring', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(1_000_000)
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  describe('a home on a shared hosting suffix', () => {
    const hosted = 'https://box-1.github.io/'
    const back = encodeURIComponent(hosted + 'shell')

    it('still signs in and stays in the tab', () => {
      const { page, asked, opened, nav } = setup(freshNav(), true, [], hosted)
      page.click()
      expect(page.fire('will-navigate', `https://provider.example/login?redirect=${back}`)).toBe(false)
      expect(asked).toEqual([`provider.example from ${hosted}`])
      page.arrive(`https://provider.example/login?redirect=${back}`)
      expect(page.fire('will-redirect', verify)).toBe(false)
      page.arrive(verify)
      expect(page.fire('will-redirect', hosted + 'shell')).toBe(false)
      expect(nav.signIn).toBeNull()
      expect(opened).toEqual([])
    })

    it('sends a neighbour\'s page to the browser with no question', () => {
      const { page, asked, opened } = setup(freshNav(), true, [], hosted)
      page.click()
      expect(page.fire('will-navigate', 'https://evil.github.io/')).toBe(true)
      expect(asked).toEqual([])
      expect(opened).toEqual(['https://evil.github.io/'])
    })

    it('asks before a neighbour\'s sign-in, and a no sends it to the browser', () => {
      const { page, asked, opened } = setup(freshNav(), false, [], hosted)
      page.click()
      const link = `https://evil.github.io/login?redirect=${back}`
      expect(page.fire('will-navigate', link)).toBe(true)
      expect(asked).toEqual([`evil.github.io from ${hosted}`])
      expect(opened).toEqual([link])
    })

    it('keeps a redirect to its own subdomain in the tab', () => {
      const { page, opened } = setup(freshNav(), true, [], hosted)
      page.click()
      page.fire('will-navigate', hosted + 'docs')
      expect(page.fire('will-redirect', 'https://api.box-1.github.io/')).toBe(false)
      expect(opened).toEqual([])
    })

    describe.each([
      ['https://box-1.github.io/', 'https://evil.github.io/'],
      ['https://10.0.1.5.nip.io/', 'https://8.8.1.5.nip.io/'],
    ])('%s beside %s', (at, neighbour) => {
      const saml = 'https://provider.example/saml'
      const ownLink = `https://provider.example/login?redirect=${encodeURIComponent(at + 'shell')}`
      const onLogin = (r: ReturnType<typeof setup>) => {
        r.page.url = at + 'login'
        return r
      }

      it('its own /login reaches a provider by a click, with the question', () => {
        const { page, nav, asked } = onLogin(setup(freshNav(), true, [], at))
        page.click()
        expect(page.fire('will-navigate', saml)).toBe(false)
        expect(asked).toEqual([`provider.example from ${at}`])
        expect(nav.signIn?.host).toBe('provider.example')
      })

      it('its own /login reaches a trusted provider with no click and no question', () => {
        const { page, nav, asked } = onLogin(setup(freshNav(), false, ['provider.example'], at))
        expect(page.fire('will-navigate', saml)).toBe(false)
        expect(asked).toEqual([])
        expect(nav.signIn?.host).toBe('provider.example')
      })

      it('its own /login with neither click nor trust goes to the browser', () => {
        const { page, nav, asked, opened } = onLogin(setup(freshNav(), true, [], at))
        expect(page.fire('will-navigate', saml)).toBe(true)
        expect(asked).toEqual([])
        expect(opened).toEqual([saml])
        expect(nav.signIn).toBeNull()
      })

      it('a neighbour\'s /login does not count as its own, click or trusted pair', () => {
        const { page, nav, asked, opened } = setup(freshNav(), true, ['provider.example'], at)
        page.url = neighbour + 'login'
        expect(page.fire('will-navigate', saml)).toBe(true)
        page.click()
        expect(page.fire('will-navigate', saml)).toBe(true)
        expect(asked).toEqual([])
        expect(opened).toEqual([saml, saml])
        expect(nav.signIn).toBeNull()
      })

      it.each(['will-navigate', 'will-redirect'] as const)('a running session meeting a neighbour by %s goes to the browser and ends', (event) => {
        const { page, nav, opened } = setup(freshNav(), true, [], at)
        page.click()
        expect(page.fire('will-navigate', ownLink)).toBe(false)
        page.arrive(ownLink)
        expect(nav.signIn?.host).toBe('provider.example')
        expect(page.fire(event, neighbour + 'shell')).toBe(true)
        expect(opened).toEqual([neighbour + 'shell'])
        expect(nav.signIn).toBeNull()
      })

      it('a redirect to a neighbour goes to the browser', () => {
        const { page, opened } = setup(freshNav(), true, [], at)
        page.click()
        expect(page.fire('will-navigate', at + 'docs')).toBe(false)
        expect(page.fire('will-redirect', neighbour)).toBe(true)
        expect(opened).toEqual([neighbour])
      })

      it('a login page\'s popup stays in on its own host and goes to the browser for a neighbour', () => {
        const { page, opened, asked } = onLogin(setup(freshNav(), true, [], at))
        const own = open(page, at + 'popup')
        expect(own.verdict.action).toBe('allow')
        const other = open(page, neighbour + 'popup')
        expect(other.verdict.action).toBe('deny')
        expect(opened).toEqual([neighbour + 'popup'])
        expect(asked).toEqual([])
      })

      it('a click on a login page asks before a neighbour\'s popup, and a no sends it to the browser', () => {
        const { page, opened, asked } = onLogin(setup(freshNav(), false, [], at))
        page.click()
        expect(open(page, neighbour + 'popup').verdict.action).toBe('deny')
        expect(asked).toEqual([`${new URL(neighbour).hostname} from ${at}`])
        expect(opened).toEqual([neighbour + 'popup'])
      })

      it('an ordinary page\'s popup to a neighbour goes to the browser with no question', () => {
        const { page, opened, asked } = setup(freshNav(), true, [], at)
        page.click()
        expect(open(page, neighbour).verdict.action).toBe('deny')
        expect(asked).toEqual([])
        expect(opened).toEqual([neighbour])
      })

      it('a listed host handing a sign-in window to a neighbour asks, and a no sends it to the browser', () => {
        const { page, opened, asked } = setup(freshNav(), false, [], at)
        page.click()
        const { child } = open(page, 'https://accounts.google.com/o')
        expect(child.fire('will-redirect', neighbour)).toBe(true)
        expect(asked).toEqual([`${new URL(neighbour).hostname} from ${at}`])
        expect(opened).toEqual([neighbour])
      })

      it('a session\'s sign-in window redirected to a neighbour goes to the browser with no question', () => {
        const { page, opened, asked } = setup(freshNav(), true, [], at)
        page.click()
        page.fire('will-navigate', ownLink)
        page.arrive(ownLink)
        const { verdict, child } = open(page, 'https://provider.example/mfa')
        expect(verdict.action).toBe('allow')
        expect(child.fire('will-redirect', neighbour)).toBe(true)
        expect(asked).toEqual([`provider.example from ${at}`])
        expect(opened).toEqual([neighbour])
      })
    })
  })

  it('opens an ordinary link in the browser and stops the tab', () => {
    const { page, opened } = setup()
    page.click()
    expect(page.fire('will-navigate', 'https://news.example.org/')).toBe(true)
    expect(opened).toEqual(['https://news.example.org/'])
  })

  it('starts a session only from a real click in the page', () => {
    const noClick = setup()
    expect(noClick.page.fire('will-navigate', signIn)).toBe(true)
    expect(noClick.nav.signIn).toBeNull()
    const hover = setup()
    hover.page.emit('input-event', {}, { type: 'mouseMove' })
    expect(hover.page.fire('will-navigate', signIn)).toBe(true)
    const late = setup()
    late.page.click()
    vi.advanceTimersByTime(GESTURE_MS + 1)
    expect(late.page.fire('will-navigate', signIn)).toBe(true)
    const clicked = setup()
    clicked.page.click()
    expect(clicked.page.fire('will-navigate', signIn)).toBe(false)
    expect(clicked.nav.signIn?.host).toBe('provider.example')
  })

  it('judges a redirect of a page navigation, which inherits its click and hop', () => {
    const { page, nav, opened } = setup()
    page.click()
    expect(page.fire('will-navigate', 'https://box-1.term.example/continue')).toBe(false)
    vi.advanceTimersByTime(GESTURE_MS + 1) // the server was slow: the click still started it
    expect(page.fire('will-redirect', signIn)).toBe(false)
    expect(nav.signIn?.host).toBe('provider.example')
    expect(page.fire('will-redirect', 'https://evil2.example/')).toBe(true)
    expect(opened).toEqual(['https://evil2.example/'])
    expect(nav.signIn).toBeNull()
  })

  it('stops a redirect off the site that nothing vouches for', () => {
    const { page, opened } = setup()
    page.click()
    page.fire('will-navigate', 'https://box-1.term.example/docs')
    expect(page.fire('will-redirect', 'https://news.example.org/')).toBe(true)
    expect(opened).toEqual(['https://news.example.org/'])
  })

  it('ignores a redirect of a subframe', () => {
    const { page, opened } = setup()
    page.click()
    page.fire('will-navigate', 'https://box-1.term.example/docs')
    expect(page.fire('will-redirect', 'https://news.example.org/', false)).toBe(false)
    expect(opened).toEqual([])
  })

  it('follows the redirects of our own load and starts a session, so the provider keeps its next step (F4)', () => {
    const { page, nav, opened } = setup({ ...freshNav(), ours: true })
    page.url = ''
    expect(page.fire('will-redirect', signIn)).toBe(false)
    expect(opened).toEqual([])
    expect(nav.signIn?.host).toBe('provider.example')
    page.arrive(signIn)
    expect(nav.ours).toBe(false)
    expect(page.fire('will-navigate', verify)).toBe(false)
    expect(page.fire('will-navigate', 'https://evil2.example/')).toBe(true)
    expect(opened).toEqual(['https://evil2.example/'])
  })

  it('keeps a running session through a reload, not through a typed load (F4)', () => {
    const { page, nav, opened } = setup()
    page.click()
    page.fire('will-navigate', signIn)
    page.arrive(signIn)
    oursKeepingSession(nav)
    expect(nav.signIn?.host).toBe('provider.example')
    expect(page.fire('will-redirect', 'https://provider.example/login/again')).toBe(false)
    page.arrive('https://provider.example/login/again')
    expect(page.fire('will-navigate', verify)).toBe(false)
    expect(opened).toEqual([])

    ours(nav)
    page.arrive('https://provider.example/somewhere')
    expect(nav.signIn).toBeNull()
    expect(page.fire('will-navigate', verify)).toBe(true)
    expect(opened).toEqual([verify])
  })

  it('judges a page navigation again after our own load', () => {
    const { page, opened } = setup({ ...freshNav(), ours: true })
    expect(page.fire('will-navigate', 'https://news.example.org/')).toBe(true)
    expect(page.fire('will-redirect', 'https://evil2.example/')).toBe(true)
    expect(opened).toEqual(['https://news.example.org/', 'https://evil2.example/'])
  })

  describe('asking about a provider', () => {
    const asksProvider = `provider.example from ${home}`

    it('asks the gate about the tab\'s home, in the page that wants to go', () => {
      const { page, askers } = setup()
      page.click()
      page.fire('will-navigate', signIn)
      expect(askers).toEqual([page])
      const inWindow = setup(freshNav(), false)
      const { child } = open(inWindow.page, 'https://accounts.google.com/o')
      child.fire('will-redirect', 'https://idp.corp.example/saml')
      expect(inWindow.askers).toEqual([child])
      expect(inWindow.asked).toEqual([`idp.corp.example from ${home}`])
    })

    it('a yes for another home does not count for this one', () => {
      const asked: string[] = []
      const page = new FakePage()
      page.url = `${home}login`
      guardTab(page as never, () => home, freshNav(), () => {}, () => ({}), fakeGate(asked, false, ['idp.corp.example'], 'https://other-home.example/'))
      page.click()
      expect(page.fire('will-navigate', 'https://idp.corp.example/saml')).toBe(true)
      expect(asked).toHaveLength(1)
    })

    it('a popup on a running session\'s provider stays in, with the session and no new question (m4)', () => {
      const { page, nav, opened, asked } = setup()
      page.click()
      page.fire('will-navigate', signIn)
      page.arrive('https://provider.example/account')
      vi.advanceTimersByTime(GESTURE_MS + 1)
      const { verdict, child } = open(page, 'https://provider.example/mfa')
      expect(verdict.action).toBe('allow')
      expect(asked).toEqual([asksProvider])
      expect(opened).toEqual([])
      expect(nav.signIn?.host).toBe('provider.example')
      expect(child.fire('will-navigate', 'https://provider.example/mfa/next')).toBe(false)
      expect(asked).toEqual([asksProvider])
    })

    it('a popup off a running session\'s provider still goes to the browser (m4)', () => {
      const { page, opened } = setup()
      page.click()
      page.fire('will-navigate', signIn)
      page.arrive('https://provider.example/account')
      expect(open(page, 'https://news.example.org/').verdict).toEqual({ action: 'deny' })
      expect(opened).toEqual(['https://news.example.org/'])
    })

    it('asks before a sign-in link stays in, then not again in that session', () => {
      const { page, nav, opened, asked } = setup()
      page.click()
      expect(page.fire('will-navigate', signIn)).toBe(false)
      expect(asked).toEqual([asksProvider])
      expect(nav.signIn?.confirmed).toBe(true)
      page.arrive(signIn)
      expect(page.fire('will-navigate', verify)).toBe(false)
      expect(page.fire('will-redirect', 'https://provider.example/step')).toBe(false)
      expect(asked).toEqual([asksProvider])
      expect(opened).toEqual([])
    })

    it('a no opens the crafted link in the browser: the tab stays put, no session', () => {
      const { page, nav, opened, asked } = setup(freshNav(), false)
      page.click()
      expect(page.fire('will-navigate', signIn)).toBe(true)
      expect(asked).toEqual([asksProvider])
      expect(opened).toEqual([signIn])
      expect(nav.signIn).toBeNull()
    })

    it('a no on a redirect stops it there too', () => {
      const { page, opened } = setup(freshNav(), false)
      page.click()
      expect(page.fire('will-navigate', 'https://box-1.term.example/continue')).toBe(false)
      expect(page.fire('will-redirect', signIn)).toBe(true)
      expect(opened).toEqual([signIn])
    })

    it('asks when a click takes the tab from its login page to its provider (F3)', () => {
      const yes = setup()
      yes.page.url = 'https://box-1.term.example/login'
      yes.page.click()
      expect(yes.page.fire('will-navigate', 'https://idp.corp.example/saml')).toBe(false)
      expect(yes.asked).toEqual([`idp.corp.example from ${home}`])
      const no = setup(freshNav(), false)
      no.page.url = 'https://box-1.term.example/login'
      no.page.click()
      expect(no.page.fire('will-navigate', 'https://idp.corp.example/saml')).toBe(true)
      expect(no.opened).toEqual(['https://idp.corp.example/saml'])
    })

    it('a redirect of our own load asks nothing until the session is what keeps a step in (F4)', () => {
      const { page, nav, opened, asked } = setup({ ...freshNav(), ours: true })
      page.url = ''
      expect(page.fire('will-redirect', signIn)).toBe(false)
      expect(page.fire('will-redirect', 'https://provider.example/next')).toBe(false)
      expect(asked).toEqual([])
      expect(nav.signIn?.confirmed).toBeUndefined()
      page.arrive('https://provider.example/next')
      expect(page.fire('will-navigate', verify)).toBe(false)
      expect(asked).toEqual([asksProvider])
      expect(opened).toEqual([])
    })

    it('after our own load, a no sends the next step to the browser and asks no more', () => {
      const { page, nav, opened, asked } = setup({ ...freshNav(), ours: true }, false)
      page.url = ''
      page.fire('will-redirect', signIn)
      page.arrive(signIn)
      expect(page.fire('will-navigate', verify)).toBe(true)
      expect(opened).toEqual([verify])
      expect(nav.signIn).toBeNull()
      expect(page.fire('will-navigate', verify)).toBe(true)
      expect(asked).toEqual([asksProvider])
    })

    it('a session moved to a new host by our own load asks about that host', () => {
      const { page, nav, asked } = setup()
      page.click()
      page.fire('will-navigate', signIn)
      page.arrive(signIn)
      ours(nav)
      expect(page.fire('will-redirect', 'https://other.example/login')).toBe(false)
      page.arrive('https://other.example/login')
      expect(page.fire('will-navigate', 'https://other.example/next')).toBe(false)
      expect(asked).toEqual([asksProvider, `other.example from ${home}`])
    })

    it('never asks about the tab\'s own site, a listed host, or a step off the session', () => {
      const { page, nav, opened, asked } = setup({ ...freshNav(), ours: true })
      page.url = ''
      page.fire('will-redirect', signIn) // an unconfirmed session on provider.example
      page.arrive(signIn)
      expect(page.fire('will-navigate', 'https://accounts.google.com/o')).toBe(false)
      page.arrive('https://accounts.google.com/o')
      expect(page.fire('will-navigate', home)).toBe(false)
      expect(nav.signIn).toBeNull()
      page.arrive(home)
      page.click()
      expect(page.fire('will-navigate', 'https://news.example.org/')).toBe(true)
      expect(opened).toEqual(['https://news.example.org/'])
      expect(asked).toEqual([])
    })

    it('never asks on a home where people post links: no session, the browser', () => {
      const nav = freshNav()
      const page = new FakePage()
      const opened: string[] = []
      const asked: string[] = []
      const gh = 'https://github.com/o/r'
      page.url = gh
      guardTab(page as never, () => gh, nav, (u) => opened.push(u), () => ({}), fakeGate(asked, true, [], gh))
      page.click()
      const link = 'https://provider.example/login?next=' + encodeURIComponent(gh)
      expect(page.fire('will-navigate', link)).toBe(true)
      expect(opened).toEqual([link])
      expect(asked).toEqual([])
    })
  })

  // M1: a listed host handing off to an unlisted one asks, with no click needed;
  // on a yes the sign-in stays in until it comes home.
  describe('federation from a listed host', () => {
    const box = 'https://box-1.term.example/'
    const portal = 'https://example-org.awsapps.com/start'
    const rows: Array<[string, string, string, string, string]> = [
      ['Microsoft to ADFS', box, 'https://login.microsoftonline.com/t/saml2', 'https://adfs.corp.example/adfs/ls', 'adfs.corp.example'],
      ['Okta to a company IdP', box, 'https://acme.okta.com/app/x/sso/saml', 'https://idp.corp.example/saml', 'idp.corp.example'],
      ['Google Workspace to third-party SSO', box, 'https://accounts.google.com/o/saml2', 'https://sso.vendor.example/saml', 'sso.vendor.example'],
      ['AWS portal to a company IdP', portal, portal, 'https://idp.corp.example/saml', 'idp.corp.example'],
      ['AWS federation to the console', portal, 'https://signin.aws.amazon.com/federation', 'https://console.aws.amazon.com/', 'console.aws.amazon.com'],
      ['GitHub OAuth to org SSO', box, 'https://github.com/login/oauth/authorize', 'https://github.com/orgs/x/sso', 'github.com'],
      ['slack.com/oauth to a workspace sign-in', box, 'https://slack.com/oauth/v2/authorize', 'https://acme.slack.com/signin', 'acme.slack.com'],
    ]

    it.each(rows)('%s: a redirect asks, then stays in on a yes', (_name, at, from, to, provider) => {
      const { page, nav, opened, asked } = setup(freshNav(), true, [], at)
      page.url = from // no click: a server redirect
      expect(page.fire('will-redirect', to)).toBe(false)
      expect(asked).toEqual([`${provider} from ${at}`])
      expect(nav.signIn?.confirmed).toBe(true)
      page.arrive(to)
      expect(page.fire('will-navigate', new URL('/next', to).href)).toBe(false)
      expect(asked).toHaveLength(1)
      expect(page.fire('will-navigate', at)).toBe(false)
      expect(nav.signIn).toBeNull()
      expect(opened).toEqual([])
    })

    it.each(rows)('%s: a no opens the browser and stops the tab', (_name, at, from, to) => {
      const { page, nav, opened } = setup(freshNav(), false, [], at)
      page.url = from
      expect(page.fire('will-redirect', to)).toBe(true)
      expect(opened).toEqual([to])
      expect(nav.signIn).toBeNull()
    })

    it('asks about a crafted tenant link, naming the attacker', () => {
      const { page, opened, asked } = setup(freshNav(), false)
      page.url = 'https://evil.okta.com/x'
      expect(page.fire('will-navigate', 'https://attacker.example/')).toBe(true)
      expect(asked).toEqual([`attacker.example from ${home}`])
      expect(opened).toEqual(['https://attacker.example/'])
    })

    it('still goes to the browser with no question on a posted-content home', () => {
      const gh = 'https://acme.atlassian.net/browse/TEAM-12'
      const { page, opened, asked } = setup(freshNav(), true, [], gh)
      page.url = 'https://acme.okta.com/app'
      expect(page.fire('will-redirect', 'https://idp.corp.example/saml')).toBe(true)
      expect(opened).toEqual(['https://idp.corp.example/saml'])
      expect(asked).toEqual([])
    })

    it('moves a confirmed session to the next provider a listed host hands it to, with its own question', () => {
      const { page, nav, opened, asked } = setup()
      page.click()
      page.fire('will-navigate', signIn)
      page.arrive('https://accounts.google.com/o')
      expect(page.fire('will-redirect', 'https://idp.corp.example/saml')).toBe(false)
      expect(asked).toEqual([`provider.example from ${home}`, `idp.corp.example from ${home}`])
      expect(nav.signIn?.host).toBe('idp.corp.example')
      expect(opened).toEqual([])
    })
  })

  // M1, M2, m3: a no holds across the tab and its windows until the user acts.
  describe('after a no', () => {
    const tenant = 'https://evil.okta.com/x'
    const no = (at = tenant) => {
      const t = setup(freshNav(), false)
      t.page.url = at
      expect(t.page.fire('will-navigate', 'https://a.example/')).toBe(true)
      expect(t.asked).toHaveLength(1)
      expect(t.opened).toEqual(['https://a.example/'])
      return t
    }
    // no with a window the page opened before the no: one opened after is denied.
    const noWithWindow = () => {
      const t = setup(freshNav(), false)
      const { child } = open(t.page, 'https://accounts.google.com/o')
      t.page.url = tenant
      expect(t.page.fire('will-navigate', 'https://a.example/')).toBe(true)
      return { ...t, child }
    }

    it.each([
      ['moving to another listed page', (i: number) => `https://tenant-${i}.okta.com/x`],
      ['reloading itself', () => tenant],
    ])('a page %s with no click: 50 rounds give at most 1 question (M1)', (_name, next) => {
      const { page, opened, asked } = setup(freshNav(), false)
      page.url = tenant
      for (let i = 0; i < 50; i++) {
        expect(page.fire('will-navigate', 'https://a.example/')).toBe(true)
        page.arrive(next(i))
        vi.advanceTimersByTime(100)
      }
      expect(asked).toHaveLength(1)
      expect(opened).toHaveLength(1)
    })

    it('a page naming a new host each round gets no question and no browser (M1)', () => {
      const { page, opened, asked } = setup(freshNav(), false)
      page.url = tenant
      for (let i = 0; i < 50; i++) {
        expect(page.fire('will-navigate', `https://host-${i}.example/`)).toBe(true)
        page.arrive(tenant)
      }
      expect(asked).toHaveLength(1)
      expect(opened).toEqual(['https://host-0.example/'])
    })

    it.each([
      ['a load of ours', ours],
      ['a reload of ours', oursKeepingSession],
    ])('%s ends it: the page may ask again (M1)', (_name, act) => {
      const { page, nav, asked } = no()
      act(nav)
      page.arrive(tenant)
      expect(page.fire('will-navigate', 'https://a.example/')).toBe(true)
      expect(asked).toHaveLength(2)
    })

    it('a crash reload leaves it standing (M1)', () => {
      const { page, nav, asked } = no()
      recovering(nav)
      page.arrive(tenant)
      page.fire('will-navigate', 'https://a.example/')
      expect(asked).toHaveLength(1)
    })

    it('10 child windows with no click are denied; a click still opens one (M2)', () => {
      const { page, opened, asked } = no()
      for (let i = 0; i < 10; i++) {
        expect(open(page, 'https://accounts.google.com/o').verdict).toEqual({ action: 'deny' })
      }
      expect(asked).toHaveLength(1)
      expect(opened).toHaveLength(1)

      vi.advanceTimersByTime(1)
      page.click()
      expect(open(page, 'https://accounts.google.com/o').verdict.action).toBe('allow')
    })

    it('before any no, child windows with no click still open', () => {
      const { page } = setup(freshNav(), false)
      for (let i = 0; i < 10; i++) {
        expect(open(page, 'https://accounts.google.com/o').verdict.action).toBe('allow')
      }
    })

    it('the tab and its windows hold one list: a no in a window holds the tab, and the reverse (M2)', () => {
      const inWindow = setup(freshNav(), false)
      const { child } = open(inWindow.page, 'https://accounts.google.com/o')
      child.fire('will-redirect', 'https://idp.corp.example/saml')
      inWindow.page.url = tenant
      expect(inWindow.page.fire('will-navigate', 'https://a.example/')).toBe(true)
      expect(inWindow.asked).toHaveLength(1)
      expect(inWindow.opened).toEqual(['https://idp.corp.example/saml'])

      const inTab = noWithWindow()
      expect(inTab.child.fire('will-redirect', 'https://idp.corp.example/saml')).toBe(true)
      expect(inTab.asked).toHaveLength(1)
      expect(inTab.opened).toEqual(['https://a.example/'])
    })

    it('emptied in place: the user acting in the tab reaches a window opened before, and the reverse (M2)', () => {
      const tabActs = setup(freshNav(), false)
      const before = open(tabActs.page, 'https://accounts.google.com/o').child
      expect(before.fire('will-redirect', 'https://idp.corp.example/saml')).toBe(true)
      ours(tabActs.nav)
      tabActs.page.arrive(home)
      before.url = 'https://accounts.google.com/o'
      expect(before.fire('will-navigate', 'https://idp.corp.example/saml')).toBe(true)
      expect(tabActs.asked).toHaveLength(2)

      const windowActs = setup(freshNav(), false)
      const it2 = open(windowActs.page, 'https://accounts.google.com/o').child
      expect(it2.fire('will-redirect', 'https://idp.corp.example/saml')).toBe(true)
      vi.advanceTimersByTime(1)
      it2.click()
      expect(it2.fire('will-navigate', 'https://idp.corp.example/saml')).toBe(true)
      expect(windowActs.asked).toHaveLength(2)
    })

    it('a click made before the no ends nothing, in the window it opened or the tab that opened it (M1, M2)', () => {
      const inWindow = setup(freshNav(), false)
      inWindow.page.click()
      const opened = open(inWindow.page, 'https://accounts.google.com/o').child
      expect(opened.fire('will-redirect', 'https://idp.corp.example/saml')).toBe(true)
      inWindow.page.url = tenant
      expect(inWindow.page.fire('will-navigate', 'https://a.example/')).toBe(true)
      expect(inWindow.asked).toHaveLength(1)

      const inTab = setup(freshNav(), false)
      inTab.page.click()
      const child = open(inTab.page, 'https://accounts.google.com/o').child
      inTab.page.url = tenant
      expect(inTab.page.fire('will-navigate', 'https://a.example/')).toBe(true)
      expect(child.fire('will-navigate', 'https://idp.corp.example/saml')).toBe(true)
      expect(inTab.asked).toHaveLength(1)
    })

    it('a page acting alone opens no more browser windows, by any route; a click still can', () => {
      const { page, child, opened } = noWithWindow()
      expect(page.fire('will-navigate', 'https://news.example.org/')).toBe(true)
      expect(page.openHandler!({ url: 'https://news.example.org/' })).toEqual({ action: 'deny' })
      expect(child.fire('will-navigate', 'https://news.example.org/')).toBe(true)
      expect(child.openHandler!({ url: 'https://news.example.org/' })).toEqual({ action: 'deny' })
      expect(opened).toEqual(['https://a.example/'])

      vi.advanceTimersByTime(1)
      page.click()
      expect(page.fire('will-navigate', 'https://news.example.org/')).toBe(true)
      expect(opened).toEqual(['https://a.example/', 'https://news.example.org/'])
    })

    it('a click in a window can still open the browser from its popups', () => {
      const { child, opened } = noWithWindow()
      vi.advanceTimersByTime(1)
      child.click()
      expect(child.openHandler!({ url: 'https://news.example.org/' })).toEqual({ action: 'deny' })
      expect(opened).toEqual(['https://a.example/', 'https://news.example.org/'])
    })

    it('a page may still move inside the app: only leaving it is held', () => {
      const { page, opened, asked } = no()
      expect(page.fire('will-navigate', 'https://accounts.google.com/o')).toBe(false)
      expect(opened).toHaveLength(1)
      expect(asked).toHaveLength(1)
    })

    it('a click on the refused host asks again, in a navigation and in a popup (m3)', () => {
      const { page, nav, opened, asked } = setup(freshNav(), false)
      page.click()
      expect(page.fire('will-navigate', signIn)).toBe(true)
      expect(opened).toEqual([signIn])
      expect(nav.signIn).toBeNull()
      vi.advanceTimersByTime(1)
      page.click()
      expect(page.fire('will-navigate', signIn)).toBe(true)
      expect(asked).toHaveLength(2)
      expect(opened).toEqual([signIn, signIn])
      vi.advanceTimersByTime(1)
      page.click()
      expect(page.openHandler!({ url: signIn })).toEqual({ action: 'deny' })
      expect(asked).toHaveLength(3)
      expect(opened).toEqual([signIn, signIn, signIn])
    })

    it('a no to one link does not cost the real sign-in: a click asks for that host (m3)', () => {
      const answers: Answer[] = ['no', 'yes']
      const { page, nav, opened, asked } = setup(freshNav(), () => answers.shift()!)
      page.url = 'https://login.microsoftonline.com/t/saml2'
      page.click()
      expect(page.fire('will-navigate', 'https://privacy.corp.example/')).toBe(true)
      vi.advanceTimersByTime(1)
      page.click()
      expect(page.fire('will-navigate', 'https://adfs.corp.example/adfs/ls')).toBe(false)
      expect(asked).toEqual([`privacy.corp.example from ${home}`, `adfs.corp.example from ${home}`])
      expect(opened).toEqual(['https://privacy.corp.example/'])
      expect(nav.signIn?.host).toBe('adfs.corp.example')
    })

    it('takes the click back: the next host gets no session from it (#5)', () => {
      const { page, nav, opened, asked } = setup(freshNav(), false)
      page.click()
      page.fire('will-navigate', signIn)
      const other = 'https://other.example/login?next=' + encodeURIComponent(home)
      expect(page.fire('will-navigate', other)).toBe(true)
      expect(nav.signIn).toBeNull()
      expect(asked).toHaveLength(1)
      expect(opened).toEqual([signIn])
    })

    it('a redirect loses the click of its navigation too (#5)', () => {
      const { page, nav } = setup(freshNav(), false)
      page.click()
      page.fire('will-navigate', 'https://box-1.term.example/continue')
      page.fire('will-redirect', signIn)
      expect(nav.hopGesture).toBe(false)
      expect(nav.hop).toBe('')
    })

    it('a refused popup ends the running session and asks no more (#7)', () => {
      const { page, nav, opened, asked } = setup({ ...freshNav(), ours: true }, false)
      page.url = ''
      page.fire('will-redirect', signIn) // an unconfirmed session on provider.example
      page.arrive(signIn)
      expect(nav.signIn?.host).toBe('provider.example')
      expect(page.openHandler!({ url: 'https://provider.example/popup' })).toEqual({ action: 'deny' })
      expect(nav.signIn).toBeNull()
      expect(page.openHandler!({ url: 'https://provider.example/popup' })).toEqual({ action: 'deny' })
      expect(asked).toHaveLength(1)
      expect(opened).toEqual(['https://provider.example/popup'])
    })

    it('a question that throws is a no (guard.ts failing open)', () => {
      const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
      const { page, nav, opened } = setup(freshNav(), () => {
        throw new Error('dialog failed')
      })
      page.click()
      expect(page.fire('will-navigate', signIn)).toBe(true)
      expect(opened).toEqual([signIn])
      expect(nav.signIn).toBeNull()
      warn.mockRestore()
    })

    it('a popup question that throws opens no window', () => {
      const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
      const { page, opened } = setup(freshNav(), () => {
        throw new Error('dialog failed')
      })
      page.click()
      expect(page.openHandler!({ url: signIn })).toEqual({ action: 'deny' })
      expect(opened).toEqual([signIn])
      warn.mockRestore()
    })
  })

  describe('a question already open elsewhere', () => {
    it('goes to the browser with nothing remembered, so the page can ask later', () => {
      let answer: Answer = 'busy'
      const { page, nav, opened, asked } = setup(freshNav(), () => answer)
      page.click()
      expect(page.fire('will-navigate', signIn)).toBe(true)
      expect(opened).toEqual([signIn])
      expect(nav.signIn).toBeNull()
      answer = 'yes'
      page.click()
      expect(page.fire('will-navigate', signIn)).toBe(false)
      expect(asked).toHaveLength(2)
    })
  })

  // #6: no click and not yet accepted goes out; an http target always does.
  describe('a login page moving by itself', () => {
    const login = 'https://box-1.term.example/login'
    const saml = 'https://idp.corp.example/saml'

    it('goes to the browser with no question when the pair is not trusted', () => {
      const { page, nav, opened, asked } = setup()
      page.url = login
      expect(page.fire('will-navigate', saml)).toBe(true)
      expect(opened).toEqual([saml])
      expect(asked).toEqual([])
      expect(nav.signIn).toBeNull()
    })

    it('stays in, asking nothing, when the user already said yes to the pair', () => {
      const { page, nav, opened, asked } = setup(freshNav(), false, ['idp.corp.example'])
      page.url = login
      expect(page.fire('will-navigate', saml)).toBe(false)
      expect(asked).toEqual([])
      expect(nav.signIn?.confirmed).toBe(true)
      expect(opened).toEqual([])
    })

    it('sends an http target to the browser, even from a click', () => {
      const { page, opened, asked } = setup()
      page.url = login
      page.click()
      expect(page.fire('will-navigate', 'http://idp.corp.example/saml')).toBe(true)
      expect(opened).toEqual(['http://idp.corp.example/saml'])
      expect(asked).toEqual([])
    })
  })

  // P1: the login-path rule goes through a session and the question, or out.
  describe('the login-path rule', () => {
    it.each(['https://github.com/auth/x', 'https://gitlab.com/sso/x', 'https://linear.app/sso/x'])('a click from %s to any site goes to the browser, no question', (at) => {
      const { page, opened, asked } = setup(freshNav(), true, [], at)
      page.click()
      expect(page.fire('will-navigate', 'https://attacker.example/')).toBe(true)
      expect(opened).toEqual(['https://attacker.example/'])
      expect(asked).toEqual([])
    })

    it('a click from another home\'s login path asks, naming the host', () => {
      const { page, opened, asked } = setup(freshNav(), false)
      page.url = 'https://box-1.term.example/auth/x'
      page.click()
      expect(page.fire('will-navigate', 'https://attacker.example/')).toBe(true)
      expect(asked).toEqual([`attacker.example from ${home}`])
      expect(opened).toEqual(['https://attacker.example/'])
    })
  })

  describe('sign-in windows', () => {
    it('asks before a sign-in popup opens; a no opens it in the browser, with no window', () => {
      const yes = setup()
      yes.page.click()
      const { verdict, child } = open(yes.page, signIn)
      expect(verdict.action).toBe('allow')
      expect(yes.asked).toEqual([`provider.example from ${home}`])
      expect(child.fire('will-redirect', 'https://provider.example/step')).toBe(false)
      expect(yes.asked).toHaveLength(1) // the window inherits the yes

      const no = setup(freshNav(), false)
      no.page.click()
      expect(open(no.page, signIn).verdict).toEqual({ action: 'deny' })
      expect(no.opened).toEqual([signIn])
    })

    it('asks in a window whose page goes on to an unlisted provider (F3)', () => {
      const { page, opened, asked } = setup(freshNav(), false)
      page.url = 'https://box-1.term.example/login'
      page.click()
      const { child } = open(page, 'https://box-1.term.example/login/popup')
      expect(child.fire('will-redirect', 'https://idp.corp.example/saml')).toBe(true)
      expect(asked).toEqual([`idp.corp.example from ${home}`])
      expect(opened).toEqual(['https://idp.corp.example/saml'])
    })

    it('hands the session and the click over to the window', () => {
      const { page, nav, opened } = setup()
      page.click()
      const { verdict, child } = open(page, signIn)
      expect(verdict).toEqual({ action: 'allow', overrideBrowserWindowOptions: { width: 520 } })
      expect(nav.signIn).toBeNull() // the window's, not the tab's
      expect(child.fire('will-redirect', 'https://provider.example/step')).toBe(false)
      expect(child.fire('will-redirect', 'https://evil2.example/')).toBe(true)
      expect(opened).toEqual(['https://evil2.example/'])
    })

    it('gives the next window nothing of the last one', () => {
      const { page, asked } = setup()
      page.click()
      open(page, signIn)
      vi.advanceTimersByTime(GESTURE_MS + 1)
      const { child } = open(page, 'https://accounts.google.com/o')
      child.url = 'https://accounts.google.com/o'
      // No session came with it: a listed host handing off starts a new one, which asks.
      expect(asked).toEqual([`provider.example from ${home}`])
      expect(child.fire('will-navigate', verify)).toBe(false)
      expect(asked).toEqual([`provider.example from ${home}`, `provider.example from ${home}`])
    })

    it('lets a window on the tab\'s login page reach its provider with the click that opened it (F3)', () => {
      const { page, opened } = setup()
      page.url = 'https://box-1.term.example/login'
      page.click()
      const { child } = open(page, 'https://box-1.term.example/login/popup')
      expect(child.fire('will-redirect', 'https://idp.corp.example/saml')).toBe(false)
      expect(child.fire('will-redirect', 'https://idp.corp.example/login')).toBe(false)
      expect(opened).toEqual([])
    })

    it('gives a window the time of the click that opened it, for its own page\'s step', () => {
      const inTime = setup()
      inTime.page.url = 'https://box-1.term.example/login'
      inTime.page.click()
      const fresh = open(inTime.page, 'https://box-1.term.example/login/popup').child
      expect(fresh.fire('will-navigate', 'https://idp.corp.example/saml')).toBe(false)
      expect(inTime.asked).toEqual([`idp.corp.example from ${home}`])
      expect(inTime.opened).toEqual([])

      const late = setup()
      late.page.url = 'https://box-1.term.example/login'
      late.page.click()
      const aged = open(late.page, 'https://box-1.term.example/login/popup').child
      vi.advanceTimersByTime(GESTURE_MS + 1)
      expect(aged.fire('will-navigate', 'https://idp.corp.example/saml')).toBe(true)
      expect(late.opened).toEqual(['https://idp.corp.example/saml'])
    })

    it('sends a popup with no click to the browser, and a window spawns none', () => {
      const { page, opened } = setup()
      expect(open(page, signIn).verdict).toEqual({ action: 'deny' })
      expect(opened).toEqual([signIn])
      const { child } = open(page, 'https://accounts.google.com/o')
      expect(child.openHandler!({ url: 'https://news.example.org/' })).toEqual({ action: 'deny' })
      expect(opened).toEqual([signIn, 'https://news.example.org/'])
      expect(child.openHandler!({ url: 'https://accounts.google.com/x' })).toEqual({ action: 'deny' })
      expect(opened).toEqual([signIn, 'https://news.example.org/'])
    })
  })
})
