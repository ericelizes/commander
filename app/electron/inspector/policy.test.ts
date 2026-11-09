import { describe, expect, it } from 'vitest'

import { chromeUserAgent, clampBounds, CRASH_LOOP_MS, decideCrash, decideHop, decideNavigation, decidePopup, decidePopupHop, isAuthUrl, noteReload, ownRedirect, permissionAllowed, postedContentHome, providerToConfirm, registrableDomain, signedIn, SIGN_IN_MAX_HOPS, SIGN_IN_MAX_MS, siteOf, viewTooSmall, type SignIn } from './policy'
import { planSync } from './reconcile'

describe('decideNavigation', () => {
  const notion = 'https://www.notion.so/acme/Spec-123'
  it.each([
    [notion, 'https://www.notion.so/acme/Other-456', 'allow'], // same origin
    [notion, 'https://acme.notion.so/x', 'allow'], // same site
    ['https://app.datadoghq.com/dash/1', 'https://docs.datadoghq.com/x', 'allow'],
    [notion, 'https://accounts.google.com/o/oauth2/auth?x=1', 'allow'], // sign-in
    ['https://linear.app/a/issue/AB-1', 'https://github.com/login/oauth/authorize', 'allow'],
    [notion, 'https://example.com/', 'external'],
    [notion, 'mailto:a@b.c', 'external'],
    [notion, 'javascript:alert(1)', 'deny'],
    [notion, 'file:///etc/passwd', 'deny'],
    [notion, 'notion://www.notion.so/x', 'deny'],
    ['http://localhost:3000/', 'http://localhost:3000/next', 'allow'],
    ['http://localhost:3000/', 'http://127.0.0.1:9999/', 'external'],
    ['https://foo.co.uk/a', 'https://bar.co.uk/b', 'external'], // co.uk is not one site
  ])('%s -> %s = %s', (home, to, want) => {
    expect(decideNavigation(home, to)).toBe(want)
  })
})

describe('sign-in chains stay in the tab', () => {
  const home = 'https://example-org.awsapps.com/start/#/device'
  it('allows identity providers and AWS sign-in hosts', () => {
    for (const u of ['https://login.microsoftonline.com/x/oauth2/authorize', 'https://login.microsoft.com/a', 'https://oidc.us-east-1.amazonaws.com/x', 'https://signin.aws/', 'https://www.amazon.com/ap/signin?x=1', 'https://d-123.awsapps.com/start'])
      expect(decideNavigation(home, u)).toBe('allow')
  })
  it('lets a sign-in page send the tab home or to another sign-in host; any other host is a session to ask about', () => {
    const hop = (to: string, from: string) => decideHop({ home: 'https://example.com/', from, to, signIn: null, gesture: false, now: 1 })
    const idp = 'https://login.microsoftonline.com/x'
    expect(hop('https://app.example.com/cb', idp)).toEqual({ decision: 'allow', signIn: null })
    expect(hop('https://accounts.google.com/o', idp)).toEqual({ decision: 'allow', signIn: null })
    expect(hop('https://other.org/cb', idp)).toEqual({ decision: 'allow', signIn: { host: 'other.org', started: 1, hops: 1 } })
    expect(hop('http://other.org/cb', idp)).toEqual({ decision: 'external', signIn: null })
    expect(hop('https://other.org/cb', 'https://example.com/')).toEqual({ decision: 'external', signIn: null })
  })
  // A crafted link (F1, F8): a listed tenant anyone can register, or a path that
  // only starts like a listed one, is no way out to an arbitrary site.
  it('gives a crafted tenant link no way on', () => {
    const gh = 'https://github.com/o/r/pull/4'
    const tenant = 'https://evil.us.auth0.com/x'
    expect(decideHop({ home: gh, from: gh, to: tenant, signIn: null, gesture: false, now: 1 })).toEqual({ decision: 'allow', signIn: null })
    expect(decideHop({ home: gh, from: tenant, to: 'https://attacker.example/', signIn: null, gesture: false, now: 1 })).toEqual({ decision: 'external', signIn: null })
    // Elsewhere it only gets a session not yet accepted: the question names the host.
    const asks = { decision: 'allow', signIn: { host: 'attacker.example', started: 1, hops: 1 } }
    expect(decideHop({ home: 'https://acme.okta.com/app', from: 'https://evil.okta.com/x', to: 'https://attacker.example/', signIn: null, gesture: false, now: 1 })).toEqual(asks)
    // A session that hit its cap on a listed host gets nothing more than that.
    const spent: SignIn = { host: 'provider.example', started: 1, hops: SIGN_IN_MAX_HOPS, confirmed: true }
    expect(decideHop({ home: 'https://box.example/', from: tenant, to: 'https://attacker.example/', signIn: spent, gesture: false, now: 1 })).toEqual(asks)
  })
  it('matches listed paths on whole segments', () => {
    for (const u of ['https://github.com/login-evil/readme', 'https://github.com/sessions-evil', 'https://slack.com/oauthx', 'https://linear.app/login.html', 'https://www.amazon.com/ap/signin-evil']) {
      expect(isAuthUrl(u), u).toBe(false)
    }
    for (const u of ['https://github.com/login', 'https://github.com/login/oauth/authorize', 'https://github.com/sessions/two-factor', 'https://slack.com/oauth/v2/authorize', 'https://www.amazon.com/ap/signin?x=1']) {
      expect(isAuthUrl(u), u).toBe(true)
    }
    const notion = 'https://www.notion.so/acme/Spec-123'
    expect(decideNavigation(notion, 'https://github.com/login-evil/readme')).toBe('external')
    expect(decideHop({ home: notion, from: 'https://github.com/login-evil/readme', to: 'https://attacker.example/', signIn: null, gesture: true, now: 1 }).decision).toBe('external')
  })
  it('opens sign-in popups as child windows', () => {
    expect(decidePopup('https://login.microsoftonline.com/x', home)).toBe('child-window')
    expect(decidePopup('https://news.example.org/', home)).toBe('external')
  })
})

describe('decidePopup', () => {
  it('opens a popup a login page opens on its own site as a sign-in window', () => {
    // Notion's "Continue with Google": a notion.so popup that then goes to Google.
    expect(decidePopup('https://www.notion.so/googlepopupredirect?x=1', 'https://www.notion.so/login')).toBe('child-window')
    expect(decidePopup('https://www.notion.so/some-page', 'https://www.notion.so/workspace/page')).toBe('external') // not from a login page
    expect(decidePopup('https://evil.example.com/x', 'https://www.notion.so/login')).toBe('external') // another site
    // Notion moved to notion.com; any site's /login page counts.
    expect(decidePopup('https://www.notion.com/googlepopupredirect', 'https://www.notion.com/login')).toBe('child-window')
    expect(decidePopup('', 'https://www.notion.com/login')).toBe('child-window') // a blank popup it fills in later
    expect(decidePopup('', 'https://www.notion.com/some-page')).toBe('deny')
  })

  it('opens sign-in popups as child windows and everything else outside', () => {
    expect(decidePopup('https://accounts.google.com/signin')).toBe('child-window')
    expect(decidePopup('https://login.microsoftonline.com/common/oauth2')).toBe('child-window')
    expect(decidePopup('https://slack.com/oauth/v2/authorize')).toBe('child-window')
    expect(decidePopup('about:blank')).toBe('child-window')
    expect(decidePopup('https://github.com/o/r/pull/4')).toBe('external')
    expect(decidePopup('mailto:x@y.z')).toBe('external')
    expect(decidePopup('javascript:void(0)')).toBe('deny')
    expect(decidePopup('not a url')).toBe('deny')
  })
  it('only treats https auth hosts as auth', () => {
    expect(isAuthUrl('http://accounts.google.com/')).toBe(false)
    expect(isAuthUrl('https://evil-accounts.google.com.example.com/')).toBe(false)
    expect(isAuthUrl('https://acme.okta.com/login')).toBe(true)
  })
})

// The sign-in session model (review il9.167.2, il9.167.5): a sign-in on an
// unlisted provider starts only when the user, on the tab's own site, opens a
// login page whose return key names the tab's exact origin. While it runs,
// every step (navigation, redirect, popup, in the tab or its sign-in window)
// stays in only on the provider's host, a known sign-in host or home.
describe('sign-in sessions', () => {
  const term = 'https://box-1.term.example/'
  const signIn = 'https://provider.example/login?redirect=' + encodeURIComponent('https://box-1.term.example/shell')
  const t0 = 1_000_000

  // walk runs a tab through steps (each [to, gesture?]) from its home and
  // returns every decision; a step not allowed leaves the tab where it was.
  function walk(home: string, steps: Array<[string, boolean?]>, start: SignIn | null = null, now = t0): string[] {
    let at = home
    let s = start
    return steps.map(([to, gesture]) => {
      const r = decideHop({ home, from: at, to, signIn: s, gesture: !!gesture, now })
      s = r.signIn
      if (r.decision === 'allow') at = to
      return r.decision + (s ? '+session' : '')
    })
  }

  it('keeps the user\'s sign-in from a hosted page in the app', () => {
    expect(walk(term, [[signIn, true], ['https://provider.example/mfa'], ['https://box-1.term.example/cb?code=1']])).toEqual(['allow+session', 'allow+session', 'allow'])
    expect(walk(term, [['https://provider.example/oauth/authorize?redirect_uri=https%3A%2F%2Fbox-1.term.example%2Fcb', true]])).toEqual(['allow+session'])
    // Started from another page of the tab's site.
    const r = decideHop({ home: term, from: 'https://box-1.term.example/sign-in-to-continue', to: signIn, signIn: null, gesture: true, now: t0 })
    expect(r.decision).toBe('allow')
    expect(r.signIn).toEqual({ host: 'provider.example', started: t0, hops: 1 })
  })

  it('keeps a multi-page sign-in in until it comes home', () => {
    expect(
      walk(term, [
        [signIn, true],
        ['https://provider.example/login/password'],
        ['https://provider.example/login/verify'],
        ['https://accounts.google.com/o/oauth2/auth'], // a known IdP inside the flow
        ['https://provider.example/login/done'],
        ['https://box-1.term.example/shell'],
        ['https://provider.example/login/password'], // session over: an ordinary link again
      ]),
    ).toEqual(['allow+session', 'allow+session', 'allow+session', 'allow+session', 'allow+session', 'allow', 'external'])
  })

  it('sends a scripted or redirected step to an unrelated site out and ends the session', () => {
    expect(walk(term, [[signIn, true], ['https://evil2.example/anything'], ['https://provider.example/login/verify']])).toEqual(['allow+session', 'external', 'external'])
    // A redirect is a step like any other (views.ts passes will-redirect here).
    const s: SignIn = { host: 'provider.example', started: t0, hops: 1 }
    expect(decideHop({ home: term, from: signIn, to: 'https://evil2.example/', signIn: s, gesture: false, now: t0 })).toEqual({ decision: 'external', signIn: null })
    // Plain http on the provider's host is not the flow.
    expect(decideHop({ home: term, from: signIn, to: 'http://provider.example/x', signIn: s, gesture: false, now: t0 }).decision).toBe('external')
  })

  it('ends a session after its time or hop cap', () => {
    const old: SignIn = { host: 'provider.example', started: t0, hops: 1 }
    expect(decideHop({ home: term, from: signIn, to: 'https://provider.example/next', signIn: old, gesture: false, now: t0 + SIGN_IN_MAX_MS + 1 })).toEqual({ decision: 'external', signIn: null })
    const busy: SignIn = { host: 'provider.example', started: t0, hops: SIGN_IN_MAX_HOPS }
    expect(decideHop({ home: term, from: signIn, to: 'https://provider.example/next', signIn: busy, gesture: false, now: t0 }).decision).toBe('external')
    const steps: Array<[string, boolean?]> = [[signIn, true], ...Array.from({ length: SIGN_IN_MAX_HOPS + 2 }, (_, i): [string] => [`https://provider.example/step/${i}`])]
    const got = walk(term, steps)
    expect(got.filter((d) => d === 'allow+session')).toHaveLength(SIGN_IN_MAX_HOPS)
    expect(got.at(-1)).toBe('external')
  })

  it('starts no session without a click, from another site, or by a redirect alone', () => {
    expect(walk(term, [[signIn]])).toEqual(['external']) // scripted, no gesture
    expect(decideHop({ home: term, from: 'https://elsewhere.example/', to: signIn, signIn: null, gesture: true, now: t0 }).decision).toBe('external')
    // A redirect started by a click on the tab's own site may start one
    // (Continue -> /continue -> 302 to the provider): views passes the click.
    expect(decideHop({ home: term, from: 'https://box-1.term.example/continue', to: signIn, signIn: null, gesture: true, now: t0 }).decision).toBe('allow')
  })

  it('still sends ordinary links from that page to the browser', () => {
    const out = (to: string, home = term) => decideHop({ home, from: home, to, signIn: null, gesture: true, now: t0 }).decision
    expect(out('https://provider.example/docs')).toBe('external')
    // A login path alone is no sign-in for this tab, nor a return key alone.
    expect(out('https://provider.example/login')).toBe('external')
    expect(out('https://provider.example/start?redirect=https%3A%2F%2Fbox-1.term.example%2F')).toBe('external')
    // A return address on another host, even on the same site, is not this tab's.
    expect(out('https://provider.example/login?redirect=https%3A%2F%2Fnews.example.org%2F')).toBe('external')
    expect(out('https://provider.example/login?return_to=https%3A%2F%2Fother-box.term.example%2F')).toBe('external')
    expect(out('https://evil.example/login?next=https%3A%2F%2Fbob.pages.example%2F', 'https://alice.pages.example/')).toBe('external')
    // Exact origin: another scheme or port of the tab's host is not its way back.
    expect(out('https://provider.example/login?next=http%3A%2F%2Fbox-1.term.example%2F')).toBe('external')
    expect(out('https://provider.example/login?next=https%3A%2F%2Fbox-1.term.example%3A8443%2F')).toBe('external')
    // Ambiguous keys do not count.
    for (const k of ['origin', 'target', 'dest', 'continue', 'callback', 'goto', 'url'])
      expect(out(`https://provider.example/login?${k}=https%3A%2F%2Fbox-1.term.example%2F`)).toBe('external')
    // Plain http sign-in pages and non-web return values do not count.
    expect(out('http://provider.example/login?next=https%3A%2F%2Fbox-1.term.example%2F')).toBe('external')
    expect(out('https://provider.example/login?next=javascript%3Aalert(1)')).toBe('external')
    expect(out('https://blog.example/auth/why-oauth-is-hard')).toBe('external')
  })

  it('reads unusual homes safely', () => {
    const out = (home: string, to: string) => decideHop({ home, from: home, to, signIn: null, gesture: true, now: t0 }).decision
    expect(out('http://10.0.0.1/', 'https://provider.example/login?next=http%3A%2F%2F192.168.0.1%2F')).toBe('external')
    expect(out('http://10.0.0.1/', 'https://provider.example/login?next=http%3A%2F%2F10.0.0.1%2Fx')).toBe('allow')
    expect(out('https://[::1]:8080/', 'https://provider.example/login?next=https%3A%2F%2F%5B%3A%3A1%5D%3A8080%2F')).toBe('allow')
    expect(out('https://[::1]:8080/', 'https://provider.example/login?next=https%3A%2F%2F%5B%3A%3A2%5D%3A8080%2F')).toBe('external')
    expect(out('not a url', 'https://provider.example/login?next=https%3A%2F%2Fbox.example%2F')).toBe('external')
  })

  // Sites where anyone posts links never start a session: there the return
  // key names the page because whoever posted the link wrote it so.
  it('starts none on a site where people post links', () => {
    const gh = 'https://github.com/o/r/pull/4'
    const evil = 'https://evil.example/login?next=https%3A%2F%2Fgithub.com%2F'
    expect(postedContentHome(gh)).toBe(true)
    expect(postedContentHome('https://acme.atlassian.net/browse/TEAM-12')).toBe(true)
    expect(postedContentHome(term)).toBe(false)
    expect(decideHop({ home: gh, from: gh, to: evil, signIn: null, gesture: true, now: t0 }).decision).toBe('external')
    expect(decidePopupHop({ home: gh, from: gh, to: evil, signIn: null, gesture: true, now: t0 }).decision).toBe('external')
    expect(decideHop({ home: gh, from: gh, to: 'https://evil.example/login', signIn: null, gesture: true, now: t0 }).decision).toBe('external')
    expect(decidePopup('https://evil.example/auth', gh)).toBe('external')
    // GitHub's own sign-in still stays in.
    expect(decideHop({ home: gh, from: gh, to: 'https://github.com/login?return_to=%2Fo%2Fr', signIn: null, gesture: true, now: t0 }).decision).toBe('allow')
  })

  it('keeps nothing in after a login page let in by another site', () => {
    // Without a session, a third-party login page is just a page: it keeps nothing in.
    const thirdParty = (from: string, to: string) => decideHop({ home: term, from, to, signIn: null, gesture: true, now: t0 })
    expect(thirdParty('https://evil.example/login?next=https%3A%2F%2Fbox-1.term.example%2F', 'https://evil2.example/anything').decision).toBe('external')
    expect(thirdParty('https://evil.example/sso', 'https://evil2.example/x').decision).toBe('external')
    // The tab's own site's login page still may send it on (an unlisted IdP), through a session.
    const tracker = 'https://app.tracker.example/a'
    const own = decideHop({ home: tracker, from: 'https://app.tracker.example/login', to: 'https://idp.corp.example/saml', signIn: null, gesture: true, now: t0 })
    expect(own).toEqual({ decision: 'allow', signIn: { host: 'idp.corp.example', started: t0, hops: 1 } })
  })

  it('opens a popup a listed host hands off, or the tab\'s login page clicks to, as a session window', () => {
    const saml = 'https://idp.corp.example/saml'
    const session = { decision: 'child-window', signIn: { host: 'idp.corp.example', started: t0, hops: 1 } }
    expect(decidePopupHop({ home: term, from: 'https://acme.okta.com/x', to: saml, signIn: null, gesture: false, now: t0 })).toEqual(session)
    expect(decidePopupHop({ home: term, from: 'https://box-1.term.example/login', to: saml, signIn: null, gesture: true, now: t0 })).toEqual(session)
    expect(decidePopupHop({ home: term, from: 'https://box-1.term.example/login', to: saml, signIn: null, gesture: false, now: t0 }).decision).toBe('external')
    expect(decidePopupHop({ home: 'https://github.com/o/r', from: 'https://acme.okta.com/x', to: saml, signIn: null, gesture: false, now: t0 }).decision).toBe('external')
  })

  it('opens the user\'s sign-in popup as a window that runs the session', () => {
    const r = decidePopupHop({ home: term, from: term, to: signIn, signIn: null, gesture: true, now: t0 })
    expect(r).toEqual({ decision: 'child-window', signIn: { host: 'provider.example', started: t0, hops: 1 } })
    // From another page of the tab's own site (opener differs from home).
    expect(decidePopupHop({ home: term, from: 'https://box-1.term.example/x', to: signIn, signIn: null, gesture: true, now: t0 }).decision).toBe('child-window')
    // Without a click, or opened by a page on another site: the browser.
    expect(decidePopupHop({ home: term, from: term, to: signIn, signIn: null, gesture: false, now: t0 }).decision).toBe('external')
    expect(decidePopupHop({ home: term, from: 'https://evil.example/', to: signIn, signIn: null, gesture: true, now: t0 }).decision).toBe('external')
    expect(decidePopupHop({ home: term, from: term, to: 'https://provider.example/pricing', signIn: null, gesture: true, now: t0 })).toEqual({ decision: 'external', signIn: null })
    // A tab signing in hands its session to the provider's own popup.
    const s: SignIn = { host: 'provider.example', started: t0, hops: 2 }
    expect(decidePopupHop({ home: term, from: signIn, to: 'https://provider.example/popup', signIn: s, gesture: false, now: t0 })).toEqual({ decision: 'child-window', signIn: s })
  })

  // F3: the tab's own login page may send it to an unlisted provider; a click
  // that does so gives the provider a session, or its next step would leave.
  it('keeps an unlisted SSO in after its first hop', () => {
    const login = 'https://box-1.term.example/login'
    const saml = 'https://idp.corp.example/saml'
    const first = decideHop({ home: term, from: login, to: saml, signIn: null, gesture: true, now: t0 })
    expect(first).toEqual({ decision: 'allow', signIn: { host: 'idp.corp.example', started: t0, hops: 1 } })
    const second = decideHop({ home: term, from: saml, to: 'https://idp.corp.example/login', signIn: first.signIn, gesture: false, now: t0 })
    expect(second.decision).toBe('allow')
    expect(decideHop({ home: term, from: saml, to: 'https://other.example/', signIn: second.signIn, gesture: false, now: t0 })).toEqual({ decision: 'external', signIn: null })
    expect(decideHop({ home: term, from: saml, to: 'https://box-1.term.example/cb', signIn: second.signIn, gesture: false, now: t0 })).toEqual({ decision: 'allow', signIn: null })
  })

  it('sends a login page\'s step out with no click, on a posted-content home, or to http', () => {
    const login = 'https://box-1.term.example/login'
    const saml = 'https://idp.corp.example/saml'
    const out = { decision: 'external', signIn: null }
    expect(decideHop({ home: term, from: login, to: saml, signIn: null, gesture: false, now: t0 })).toEqual(out)
    const jira = 'https://acme.atlassian.net/login'
    expect(decideHop({ home: jira, from: jira, to: saml, signIn: null, gesture: true, now: t0 })).toEqual(out)
    expect(decideHop({ home: term, from: login, to: 'http://idp.corp.example/saml', signIn: null, gesture: true, now: t0 })).toEqual(out)
    // With no click, a yes already given for this home and provider starts one.
    const trusted = (p: string) => p === 'idp.corp.example'
    expect(decideHop({ home: term, from: login, to: saml, signIn: null, gesture: false, now: t0, trusted })).toEqual({ decision: 'allow', signIn: { host: 'idp.corp.example', started: t0, hops: 1 } })
    expect(decideHop({ home: term, from: login, to: 'https://other.example/', signIn: null, gesture: false, now: t0, trusted })).toEqual(out)
    // The login-path rule (P1): a path alone on a posted-content home gives no way out.
    for (const from of ['https://github.com/auth/x', 'https://gitlab.com/sso/x', 'https://linear.app/sso/x']) {
      expect(decideHop({ home: from, from, to: 'https://attacker.example/', signIn: null, gesture: true, now: t0 }), from).toEqual(out)
    }
    // A listed host needs none.
    expect(decideHop({ home: term, from: login, to: 'https://accounts.google.com/o', signIn: null, gesture: true, now: t0 })).toEqual({ decision: 'allow', signIn: null })
  })

  // F4: the redirects of a load we started are followed unjudged, so the
  // provider they land on gets a session with no click.
  it('starts a session from a redirect of the tab\'s own load', () => {
    const r = ownRedirect({ home: term, to: signIn, signIn: null, now: t0 })
    expect(r).toEqual({ host: 'provider.example', started: t0, hops: 1 })
    expect(decideHop({ home: term, from: signIn, to: 'https://provider.example/login/verify', signIn: r, gesture: false, now: t0 }).decision).toBe('allow')
    // A SAML-shaped address with no return key counts too: the page is already in the tab.
    expect(ownRedirect({ home: term, to: 'https://idp.corp.example/saml?SAMLRequest=x', signIn: null, now: t0 })?.host).toBe('idp.corp.example')
    // A chain of redirects: the last host holds it.
    const chained = ownRedirect({ home: term, to: signIn, signIn: { host: 'short.example', started: t0, hops: 1 }, now: t0 })
    expect(chained?.host).toBe('provider.example')
  })

  it('starts none from an own load that stays home, on a listed host, on http or on a posted-content home', () => {
    expect(ownRedirect({ home: term, to: 'https://box-1.term.example/start', signIn: null, now: t0 })).toBeNull()
    expect(ownRedirect({ home: term, to: 'https://accounts.google.com/o', signIn: null, now: t0 })).toBeNull()
    expect(ownRedirect({ home: term, to: 'http://provider.example/login', signIn: null, now: t0 })).toBeNull()
    expect(ownRedirect({ home: 'https://github.com/o/r', to: signIn, signIn: null, now: t0 })).toBeNull()
  })

  it('counts an own redirect as a step of a running session and ends it at home', () => {
    const s: SignIn = { host: 'provider.example', started: t0, hops: 3 }
    expect(ownRedirect({ home: term, to: 'https://provider.example/again', signIn: s, now: t0 })).toEqual({ ...s, hops: 4 })
    expect(ownRedirect({ home: term, to: 'https://box-1.term.example/cb', signIn: s, now: t0 })).toBeNull()
  })

  // F5: a login page on the tab's own site stays in, with no session that
  // would then refuse the provider it sends the tab to.
  it('starts no session for a target on the home site', () => {
    const own = 'https://box-1.term.example/login?next=https%3A%2F%2Fbox-1.term.example%2F'
    expect(decideHop({ home: term, from: term, to: own, signIn: null, gesture: true, now: t0 })).toEqual({ decision: 'allow', signIn: null })
    expect(decidePopupHop({ home: term, from: term, to: own, signIn: null, gesture: true, now: t0 })).toEqual({ decision: 'child-window', signIn: null })
    expect(decideHop({ home: term, from: own, to: 'https://idp.corp.example/saml', signIn: null, gesture: true, now: t0 }).decision).toBe('allow')
  })

  // m4: a popup honours the session's provider host, as a navigation does.
  it('keeps a popup on a running session\'s https provider host, handing it the session', () => {
    const s: SignIn = { host: 'provider.example', started: t0, hops: 3 }
    const at = { home: term, from: 'https://provider.example/account', gesture: false, now: t0 }
    expect(decidePopupHop({ ...at, to: 'https://provider.example/mfa', signIn: s })).toEqual({ decision: 'child-window', signIn: s })
    expect(decidePopupHop({ ...at, to: 'http://provider.example/mfa', signIn: s }).decision).toBe('external')
    expect(decidePopupHop({ ...at, to: 'https://sub.provider.example/mfa', signIn: s }).decision).toBe('external')
    expect(decidePopupHop({ ...at, to: 'https://provider.example/mfa', signIn: null }).decision).toBe('external')
    expect(decidePopupHop({ ...at, to: 'https://provider.example/mfa', signIn: { ...s, started: t0 - SIGN_IN_MAX_MS - 1 } }).decision).toBe('external')
  })

  it('guards the sign-in window like the tab', () => {
    // The window's steps run under its session: the provider and home stay, others go out.
    const s: SignIn = { host: 'provider.example', started: t0, hops: 1 }
    expect(decideHop({ home: term, from: signIn, to: 'https://provider.example/login/verify', signIn: s, gesture: false, now: t0 }).decision).toBe('allow')
    expect(decideHop({ home: term, from: signIn, to: 'https://box-1.term.example/cb', signIn: s, gesture: false, now: t0 })).toEqual({ decision: 'allow', signIn: null })
    expect(decideHop({ home: term, from: signIn, to: 'https://evil2.example/', signIn: s, gesture: false, now: t0 }).decision).toBe('external')
    // A window with no session (a known IdP's popup) keeps only sign-in hosts and home's site.
    expect(decideHop({ home: term, from: 'about:blank', to: 'https://accounts.google.com/x', signIn: null, gesture: false, now: t0 }).decision).toBe('allow')
    expect(decideHop({ home: term, from: 'about:blank', to: 'https://evil2.example/', signIn: null, gesture: false, now: t0 }).decision).toBe('external')
    // Its own popups: only 'external' opens (in the browser).
    expect(decidePopup('https://provider.example/login')).toBe('external')
    expect(decidePopup('https://provider.example/login?next=https%3A%2F%2Fbox-1.term.example%2F')).toBe('external')
  })
})

describe('permissions, UA, site', () => {
  it('denies by default', () => {
    for (const p of ['media', 'geolocation', 'notifications', 'midi', 'openExternal', 'clipboard-read']) {
      expect(permissionAllowed(p)).toBe(false)
    }
    expect(permissionAllowed('clipboard-sanitized-write')).toBe(true)
  })
  it('sends a plain Chrome UA per platform', () => {
    const mac = chromeUserAgent('darwin', '140.0.7339.80')
    expect(mac).toContain('Macintosh')
    expect(mac).toContain('Chrome/140.0.0.0')
    expect(mac).not.toMatch(/Electron/)
    expect(chromeUserAgent('linux', '140.1')).toContain('X11; Linux x86_64')
  })
  it('groups subdomains into a site', () => {
    expect(siteOf('app.datadoghq.com')).toBe('datadoghq.com')
    expect(siteOf('www.bbc.co.uk')).toBe('bbc.co.uk')
    expect(siteOf('localhost')).toBe('localhost')
  })
})

describe('signedIn', () => {
  it('detects session cookies per site', () => {
    const now = 1_000_000
    const got = signedIn(
      [
        { name: 'token_v2', domain: '.www.notion.so', value: 'abc' },
        { name: 'logged_in', domain: '.github.com', value: 'no' },
        { name: 'dogweb', domain: 'app.datadoghq.com', value: 'x', expirationDate: now - 1 }, // expired
        { name: 'd', domain: '.slack.com', value: 'xoxd-1' },
        { name: 'd', domain: '.notslack.com', value: 'x' },
      ],
      now,
    )
    expect(got).toEqual({ notion: true, linear: false, github: false, datadog: false, slack: true })
    expect(signedIn([{ name: 'logged_in', domain: '.github.com', value: 'yes' }]).github).toBe(true)
  })
})

describe('clampBounds', () => {
  it('rounds and keeps the view inside the window', () => {
    expect(clampBounds({ x: 10.4, y: 20.6, width: 300.2, height: 400.7 }, { width: 1000, height: 800 })).toEqual({ x: 10, y: 21, width: 300, height: 401 })
    expect(clampBounds({ x: 900, y: 700, width: 300, height: 300 }, { width: 1000, height: 800 })).toEqual({ x: 900, y: 700, width: 100, height: 100 })
    expect(clampBounds({ x: -5, y: -5, width: 10, height: 10 }, { width: 100, height: 100 })).toEqual({ x: 0, y: 0, width: 10, height: 10 })
  })
})

describe('planSync', () => {
  const rect = { x: 0, y: 0, width: 100, height: 100 }
  const tabs = [
    { id: 1, url: 'https://a.com' },
    { id: 2, url: 'https://b.com' },
  ]
  it('creates lazily: only the active tab, only when shown', () => {
    expect(planSync(new Map(), { tabs, activeId: 1, rect })).toEqual({ create: [1], destroy: [], reload: [], show: 1 })
    expect(planSync(new Map(), { tabs, activeId: 1, rect: null })).toEqual({ create: [], destroy: [], reload: [], show: null })
    expect(planSync(new Map(), { tabs, activeId: null, rect })).toEqual({ create: [], destroy: [], reload: [], show: null })
  })
  it('keeps hidden views alive and destroys removed ones', () => {
    const existing = new Map([
      [1, 'https://a.com'],
      [3, 'https://c.com'],
    ])
    expect(planSync(existing, { tabs, activeId: 2, rect })).toEqual({ create: [2], destroy: [3], reload: [], show: 2 })
  })
  it('reloads a view whose tab target changed', () => {
    expect(planSync(new Map([[1, 'https://old.com']]), { tabs, activeId: 1, rect }).reload).toEqual([{ id: 1, url: 'https://a.com' }])
  })
  it('does not show into an empty rect', () => {
    expect(planSync(new Map(), { tabs, activeId: 1, rect: { x: 0, y: 0, width: 0, height: 10 } }).show).toBeNull()
  })
})

describe('decideCrash', () => {
  const t0 = 1_000_000
  it('reloads a first crash and a crash long after the last reload', () => {
    expect(decideCrash({ lastReloadAt: null, reloads: 0 }, t0)).toBe('reload')
    expect(decideCrash({ lastReloadAt: t0, reloads: 3 }, t0 + 60_000)).toBe('reload')
  })

  it('gives up when it crashes again within 10s of a reload', () => {
    expect(decideCrash({ lastReloadAt: t0, reloads: 1 }, t0 + 9_999)).toBe('give-up')
    expect(decideCrash({ lastReloadAt: t0, reloads: 1 }, t0 + CRASH_LOOP_MS)).toBe('reload')
  })

  it('caps reloads for spaced-out repeated crashes', () => {
    let r = { lastReloadAt: null as number | null, reloads: 0 }
    let now = t0
    const seen: string[] = []
    for (let i = 0; i < 5; i++) {
      const d = decideCrash(r, now)
      seen.push(d)
      if (d === 'give-up') break
      r = noteReload(r, now)
      now += 20_000
    }
    expect(seen).toEqual(['reload', 'reload', 'reload', 'give-up'])
  })

  it('noteReload starts a fresh count once the last reload is old', () => {
    expect(noteReload({ lastReloadAt: t0, reloads: 3 }, t0 + 60_000)).toEqual({ lastReloadAt: t0 + 60_000, reloads: 1 })
    expect(noteReload({ lastReloadAt: t0, reloads: 1 }, t0 + 20_000)).toEqual({ lastReloadAt: t0 + 20_000, reloads: 2 })
  })
})

describe('notion domains and tiny views', () => {
  it('keeps app.notion.com, www.notion.so and notion.site together', () => {
    const home = 'https://app.notion.com/p/0123456789abcdef0123456789abcdef'
    expect(decideNavigation(home, 'https://www.notion.so/Spec-0123456789abcdef0123456789abcdef')).toBe('allow')
    expect(decideNavigation('http://www.notion.so/Spec-1', 'https://app.notion.com/p/1')).toBe('allow')
    expect(decideNavigation(home, 'https://acme.notion.site/x')).toBe('allow')
    expect(decideNavigation(home, 'https://notion.evil.example/x')).toBe('external')
    expect(siteOf('app.notion.com')).toBe(siteOf('www.notion.so'))
  })
  it('sees a Notion sign-in on notion.com cookies', () => {
    const c = [{ name: 'token_v2', domain: '.notion.com', value: 'x' }]
    expect(signedIn(c).notion).toBe(true)
  })
  it('reports a view under 200px as too small', () => {
    expect(viewTooSmall({ x: 0, y: 0, width: 150, height: 600 })).toBe(true)
    expect(viewTooSmall({ x: 0, y: 0, width: 600, height: 0 })).toBe(true)
    expect(viewTooSmall({ x: 0, y: 0, width: 200, height: 200 })).toBe(false)
  })
})

// Hosts are invented except the suffix under test. Each row runs the real policy.
describe('site boundaries', () => {
  const neighbours: Array<[home: string, target: string]> = [
    ['https://a.vercel.app/', 'https://evil.vercel.app/'],
    ['https://alice.github.io/', 'https://evil.github.io/'],
    ['https://box-1.github.io/', 'https://evil.github.io/'],
    ['https://box-1.github.io/', 'https://github.io/'],
    ['https://a.pages.dev/', 'https://evil.pages.dev/'],
    ['https://a.web.app/', 'https://evil.web.app/'],
    ['https://a.modal.run/', 'https://evil.modal.run/'],
    ['https://a.gradio.live/', 'https://evil.gradio.live/'],
    ['http://10.0.1.5/', 'http://8.8.1.5/'],
    ['http://10.0.1.5/', 'http://10.0.1.6/'],
    ['http://[2001:db8::1]/', 'http://[2001:db8::2]/'],
    ['https://example.com/', 'https://example.co.uk/'],
    ['https://a.co.uk/', 'https://b.co.uk/'],
    ['https://a.nip.io/', 'https://evil.nip.io/'],
    ['https://10.0.1.5.nip.io/', 'https://8.8.8.8.nip.io/'],
    ['https://10.0.1.5.nip.io/', 'https://8.8.1.5.nip.io/'],
    ['https://a.nip.io/', 'https://nip.io/'],
    ['https://a.nip.io/', 'https://api.a.nip.io/'],
    ['https://a.xip.io/', 'https://evil.xip.io/'],
    ['https://10.0.1.5.xip.io/', 'https://8.8.1.5.xip.io/'],
    ['https://a.sslip.io/', 'https://evil.sslip.io/'],
    ['https://10-0-1-5.sslip.io/', 'https://8-8-1-5.sslip.io/'],
    ['https://a.app.github.dev/', 'https://evil.app.github.dev/'],
    ['https://a.loca.lt/', 'https://evil.loca.lt/'],
    ['https://a.gitpod.io/', 'https://evil.gitpod.io/'],
    ['https://a.devtunnels.ms/', 'https://evil.devtunnels.ms/'],
    ['https://a.lhr.life/', 'https://evil.lhr.life/'],
    ['https://a.koyeb.app/', 'https://evil.koyeb.app/'],
    ['https://a.railway.app/', 'https://evil.railway.app/'],
    ['https://a.up.railway.app/', 'https://evil.up.railway.app/'],
    ['https://a.codesandbox.io/', 'https://evil.codesandbox.io/'],
    ['https://a.rf.gd/', 'https://evil.rf.gd/'],
    ['https://a.us.to/', 'https://evil.us.to/'],
    ['https://a.pgs.sh/', 'https://evil.pgs.sh/'],
    ['https://a.ply.gg/', 'https://evil.ply.gg/'],
    ['https://a.tun.ms/', 'https://evil.tun.ms/'],
    ['https://a.lvh.me/', 'https://evil.lvh.me/'],
    ['https://a.app.github.dev/', 'https://github.dev/'], // a Codespaces port and the sign-in host it hands to
    ['https://gitpod.io/', 'https://api.gitpod.io/'], // the cost of a suffix entry: the platform's own site splits
  ]
  it.each(neighbours)('%s sends %s to the browser', (home, target) => {
    expect(decideNavigation(home, target)).toBe('external')
  })

  const oneSite: Array<[home: string, target: string]> = [
    ['https://app.datadoghq.com/', 'https://docs.datadoghq.com/'],
    ['https://www.bbc.co.uk/', 'https://news.bbc.co.uk/'],
    ['https://box-1.github.io/', 'https://api.box-1.github.io/'],
    ['https://a.vercel.app/', 'https://www.a.vercel.app/'],
    ['https://a.modal.run/', 'https://api.a.modal.run/'],
    ['https://a.loca.lt/', 'https://api.a.loca.lt/'],
    ['https://a.up.railway.app/', 'https://api.a.up.railway.app/'],
    ['https://a.nip.io/', 'https://A.NIP.IO./'],
    ['https://a.nip.io:3000/', 'https://a.nip.io:4000/'],
    ['http://10.0.1.5:3000/', 'http://10.0.1.5:4000/'],
    ['https://example.com/', 'https://APP.Example.COM./'],
    ['https://hex.pm/', 'https://sso.hex.pm/'],
    ['https://www.ibm.io/', 'https://app.ibm.io/'],
    ['https://intranet.corp.internal/', 'https://wiki.corp.internal/'],
  ]
  it.each(oneSite)('%s keeps %s in the tab', (home, target) => {
    expect(decideNavigation(home, target)).toBe('allow')
  })

  // Known limits, pinned so a change to them is seen: these stay in with no question.
  // atlassian.net cannot be a suffix entry, postedContentHome compares siteOf on it;
  // notion.site stays in the alias by decision; a region label reads as the name;
  // a short name under a country suffix that neither list carries reads as one site.
  const knownLimits: Array<[home: string, target: string]> = [
    ['https://acme.atlassian.net/', 'https://other.atlassian.net/'],
    ['https://www.notion.so/', 'https://evil.notion.site/'],
    ['https://a.usw2.devtunnels.ms/', 'https://evil.usw2.devtunnels.ms/'],
    ['https://a.ws-eu1.gitpod.io/', 'https://evil.ws-eu1.gitpod.io/'],
    ['https://a.xyz.io/', 'https://evil.xyz.io/'],
  ]
  it.each(knownLimits)('known limit: %s keeps %s in the tab', (home, target) => {
    expect(decideNavigation(home, target)).toBe('allow')
  })

  const sites: Array<[host: string, site: string]> = [
    ['a.vercel.app', 'a.vercel.app'],
    ['deep.a.vercel.app', 'a.vercel.app'],
    ['alice.github.io', 'alice.github.io'],
    ['a.b.github.io', 'b.github.io'],
    ['github.io', 'github.io'],
    ['deep.a.modal.run', 'a.modal.run'],
    ['modal.run', 'modal.run'],
    ['10.0.1.5.nip.io', '10.0.1.5.nip.io'],
    ['api.a.nip.io', 'api.a.nip.io'],
    ['nip.io', 'nip.io'],
    ['A.SSLIP.IO.', 'a.sslip.io'],
    ['deep.a.loca.lt', 'a.loca.lt'],
    ['deep.a.app.github.dev', 'a.app.github.dev'],
    ['a.railway.app', 'a.railway.app'],
    ['deep.a.up.railway.app', 'a.up.railway.app'],
    ['up.railway.app', 'up.railway.app'],
    ['8.8.1.5', '8.8.1.5'],
    ['[2001:db8::1]', '[2001:db8::1]'],
    ['WWW.BBC.CO.UK.', 'bbc.co.uk'],
    ['intranet', 'intranet'],
    ['com', 'com'],
    ['', ''],
    ['app.notion.com', 'notion.so'],
    ['acme.notion.site', 'notion.so'],
  ]
  it.each(sites)('reads %s as %s', (host, site) => {
    expect(siteOf(host)).toBe(site)
  })

  const domains: Array<[host: string, domain: string]> = [
    ['evil.notion.site', 'evil.notion.site'],
    ['deep.evil.notion.site', 'evil.notion.site'],
    ['app.notion.com', 'notion.com'],
    ['www.notion.so', 'notion.so'],
    ['evil.vercel.app', 'evil.vercel.app'],
    ['8.8.1.5', '8.8.1.5'],
  ]
  it.each(domains)('names %s by its real domain %s', (host, domain) => {
    expect(registrableDomain(host)).toBe(domain)
  })

  it('does not let a login page open a neighbour as a sign-in window', () => {
    expect(decidePopup('https://evil.vercel.app/x', 'https://a.vercel.app/login')).toBe('external')
    expect(decidePopup('https://a.vercel.app/x', 'https://a.vercel.app/login')).toBe('child-window')
    expect(decidePopup('https://8.8.1.5/x', 'https://10.0.1.5/login')).toBe('external')
  })

  it('gives an IP-embedding neighbour none of the home\'s standing', () => {
    const home = 'https://10.0.1.5.nip.io/'
    const evil = 'https://8.8.1.5.nip.io/'
    expect(decidePopup(evil + 'x', home + 'login')).toBe('external')
    const fromEvil = { home, from: evil + 'login', to: 'https://provider.example/x', signIn: null, now: 1, trusted: () => true }
    expect(decideHop({ ...fromEvil, gesture: true })).toEqual({ decision: 'external', signIn: null })
    expect(decideHop({ ...fromEvil, gesture: false })).toEqual({ decision: 'external', signIn: null })
    const running: SignIn = { host: 'provider.example', started: 1, hops: 1 }
    expect(decideHop({ home, from: 'https://provider.example/x', to: evil, signIn: running, gesture: false, now: 1 })).toEqual({ decision: 'external', signIn: null })
  })

  it('does not let a neighbour use a pair trusted for the home', () => {
    const r = decideHop({ home: 'https://a.vercel.app/', from: 'https://evil.vercel.app/login', to: 'https://provider.example/x', signIn: null, gesture: false, now: 1, trusted: () => true })
    expect(r).toEqual({ decision: 'external', signIn: null })
  })

  it('makes a neighbour\'s sign-in link a session with its question, not a same-site step', () => {
    const home = 'https://box-1.github.io/'
    const link = 'https://evil.github.io/login?redirect=' + encodeURIComponent(home)
    const clicked = decideHop({ home, from: home, to: link, signIn: null, gesture: true, now: 1 })
    expect(clicked).toEqual({ decision: 'allow', signIn: { host: 'evil.github.io', started: 1, hops: 1 } })
    expect(providerToConfirm(link, clicked.signIn)).toBe('evil.github.io')
    expect(decideHop({ home, from: home, to: link, signIn: null, gesture: false, now: 1 })).toEqual({ decision: 'external', signIn: null })
  })

  it('still judges the sites people post on, and no hosted page, as posted-content homes', () => {
    for (const home of ['https://acme.atlassian.net/', 'https://gitlab.com/x', 'https://github.com/x', 'https://app.slack.com/', 'https://acme.notion.site/']) {
      expect(postedContentHome(home)).toBe(true)
    }
    for (const home of ['https://alice.github.io/', 'https://box-1.github.io/', 'https://a.vercel.app/', 'http://10.0.1.5/', 'https://a.nip.io/', 'https://a.app.github.dev/']) {
      expect(postedContentHome(home)).toBe(false)
    }
  })
})

describe('providerToConfirm', () => {
  const s: SignIn = { host: 'provider.example', started: 1, hops: 1 }

  it('names the session\'s host until it is confirmed', () => {
    expect(providerToConfirm('https://provider.example/login', s)).toBe('provider.example')
    expect(providerToConfirm('https://provider.example/login', { ...s, confirmed: true })).toBeNull()
  })

  it('names nobody with no session, or for a step off its host', () => {
    expect(providerToConfirm('https://provider.example/login', null)).toBeNull()
    expect(providerToConfirm('https://accounts.google.com/o', s)).toBeNull()
    expect(providerToConfirm('https://sub.provider.example/', s)).toBeNull()
    expect(providerToConfirm('not a url', s)).toBeNull()
  })
})
