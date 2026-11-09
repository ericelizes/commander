import * as fs from 'node:fs'
import * as os from 'node:os'
import * as path from 'node:path'

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { Answer } from './guard'
import { askInWindow, providerDialog, providerGate, CONTINUE_HERE, trustFile, type DialogWindow } from './trust'

const home = 'https://box-1.term.example/shell'
const asker = {} as never
let dir: string
let file: string

beforeEach(() => {
  dir = fs.mkdtempSync(path.join(os.tmpdir(), 'trust-'))
  file = trustFile(dir)
})
afterEach(() => {
  fs.rmSync(dir, { recursive: true, force: true })
})

describe('providerGate', () => {
  it('saves a yes as a pair of exact hosts, also across a restart', () => {
    const ask = vi.fn(() => true)
    expect(providerGate(file, ask).trusted('provider.example', home)).toBe(false)
    expect(providerGate(file, ask).ask('provider.example', home, asker)).toBe('yes')
    expect(ask).toHaveBeenCalledWith('provider.example', home, asker)
    expect(providerGate(file, ask).trusted('provider.example', 'https://box-1.term.example/other')).toBe(true)
    expect(JSON.parse(fs.readFileSync(file, 'utf8'))).toEqual([{ home: 'box-1.term.example', provider: 'provider.example' }])
    expect(fs.statSync(file).mode & 0o777).toBe(0o600)
  })

  it('trusts the pair only: another home or provider, even on the same site, is its own question (#2)', () => {
    const gate = providerGate(file, () => true)
    gate.ask('acme.saas.example', home, asker)
    expect(gate.trusted('acme.saas.example', home)).toBe(true)
    expect(gate.trusted('evil.saas.example', home)).toBe(false)
    expect(gate.trusted('acme.saas.example', 'https://mail.example/')).toBe(false)
    expect(gate.trusted('acme.saas.example', 'https://box-2.term.example/')).toBe(false)
  })

  it('reads an old bare entry as not trusted, and the next yes drops it (#2)', () => {
    fs.writeFileSync(file, JSON.stringify(['provider.example']))
    const gate = providerGate(file, () => true)
    expect(gate.trusted('provider.example', home)).toBe(false)
    gate.ask('provider.example', home, asker)
    expect(JSON.parse(fs.readFileSync(file, 'utf8'))).toEqual([{ home: 'box-1.term.example', provider: 'provider.example' }])
  })

  it('remembers no refusal in the file', () => {
    const ask = vi.fn(() => false)
    const gate = providerGate(file, ask)
    expect(gate.ask('provider.example', home, asker)).toBe('no')
    expect(gate.ask('provider.example', home, asker)).toBe('no')
    expect(ask).toHaveBeenCalledTimes(2)
    expect(fs.existsSync(file)).toBe(false)
  })

  it('keeps what another window saved since this one started', () => {
    const one = providerGate(file, () => true)
    const two = providerGate(file, () => true)
    one.ask('provider.example', home, asker)
    two.ask('idp.corp.example', home, asker)
    expect(one.trusted('idp.corp.example', home)).toBe(true)
    expect(two.trusted('provider.example', home)).toBe(true)
  })

  it.each(['{broken', '{"provider.example":true}', '"provider.example"', '[1,null]', '[{"home":"box-1.term.example"}]', '[{"home":1,"provider":"provider.example"}]'])('trusts nobody on a damaged file: %s', (raw) => {
    fs.writeFileSync(file, raw)
    expect(providerGate(file, () => false).trusted('provider.example', home)).toBe(false)
  })

  it('lets this sign-in go on when the answer cannot be saved, and asks next time', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    const gate = providerGate(path.join(dir, 'gone', 'trusted.json'), () => true)
    expect(gate.ask('provider.example', home, asker)).toBe('yes')
    expect(gate.trusted('provider.example', home)).toBe(false)
    expect(warn).toHaveBeenCalled()
    warn.mockRestore()
  })

  it('holds one question at a time across every window\'s gate; the second is not asked (#5)', () => {
    const inner = vi.fn(() => true)
    const otherWindow = providerGate(file, inner)
    let during: Answer | null = null
    const gate = providerGate(file, () => {
      during = otherWindow.ask('idp.corp.example', home, asker)
      return false
    })
    expect(gate.ask('provider.example', home, asker)).toBe('no')
    expect(during).toBe('busy')
    expect(inner).not.toHaveBeenCalled()
    expect(otherWindow.ask('idp.corp.example', home, asker)).toBe('yes')
  })

  it('frees the question when the dialog throws', () => {
    const gate = providerGate(file, () => {
      throw new Error('dialog failed')
    })
    expect(() => gate.ask('provider.example', home, asker)).toThrow('dialog failed')
    expect(providerGate(file, () => true).ask('provider.example', home, asker)).toBe('yes')
  })
})

describe('askInWindow', () => {
  type Win = DialogWindow & { name: string }
  const win = (name: string, destroyed = false): Win => ({ name, isDestroyed: () => destroyed })

  function run(owner: Win | null, main: Win, button = CONTINUE_HERE) {
    const parents: string[] = []
    const windowOf = vi.fn(() => owner)
    const ask = askInWindow(main, windowOf, (parent, options) => {
      parents.push(parent.name)
      expect(options.message).toContain('provider.example')
      return button
    })
    return { answer: ask('provider.example', home, asker), parents, windowOf }
  }

  it('parents the dialog to the sign-in window that asked', () => {
    const r = run(win('sign-in'), win('main'))
    expect(r.parents).toEqual(['sign-in'])
    expect(r.windowOf).toHaveBeenCalledWith(asker)
    expect(r.answer).toBe(true)
  })

  it('parents a tab\'s question to the main window: a view has no window of its own', () => {
    expect(run(null, win('main')).parents).toEqual(['main'])
  })

  it('falls back to main when the sign-in window is gone', () => {
    expect(run(win('sign-in', true), win('main')).parents).toEqual(['main'])
  })

  it('answers no with no window left, and on any button but Continue here', () => {
    const gone = run(null, win('main', true))
    expect(gone.answer).toBe(false)
    expect(gone.parents).toEqual([])
    expect(run(null, win('main'), 0).answer).toBe(false)
  })
})

describe('providerDialog', () => {
  it('names the provider and the tab; the default and Esc are the browser', () => {
    const d = providerDialog('provider.example', 'https://box-1.term.example/shell?x=1')
    expect(d.message).toBe('Continue to provider.example in this tab?')
    expect(d.title).toBe('Continue here?')
    expect(d.detail).toContain('The box-1.term.example tab')
    expect(d.detail).not.toContain('shell?x=1')
    const buttons = d.buttons ?? []
    expect(buttons[d.defaultId!]).toBe('Open in browser')
    expect(buttons[d.cancelId!]).toBe('Open in browser')
    expect(buttons[CONTINUE_HERE]).toBe('Continue here')
  })

  it('leads with the registrable domain, then the full host (#9)', () => {
    const d = providerDialog('login.okta.com.account.verify.evil.example', home)
    expect(d.message).toBe('Continue to evil.example (login.okta.com.account.verify.evil.example) in this tab?')
    expect(d.detail).toContain('going to evil.example (login.okta.com.account.verify.evil.example)')
  })

  it.each([
    ['evil.notion.site', 'evil.notion.site'],
    ['deep.evil.notion.site', 'evil.notion.site (deep.evil.notion.site)'],
    ['app.notion.com', 'notion.com (app.notion.com)'],
    ['evil.vercel.app', 'evil.vercel.app'],
    ['deep.evil.github.io', 'evil.github.io (deep.evil.github.io)'],
    ['evil-80.app.github.dev', 'evil-80.app.github.dev'],
    ['deep.evil.loca.lt', 'evil.loca.lt (deep.evil.loca.lt)'],
    ['8.8.1.5', '8.8.1.5'],
  ])('leads %s with its true domain, never the suffix or an alias', (provider, named) => {
    expect(providerDialog(provider, home).message).toBe(`Continue to ${named} in this tab?`)
  })

  it.each([
    ['accounts.google.com.8.8.8.8.nip.io', 'nip.io (accounts.google.com.8.8.8.8.nip.io)'],
    ['accounts.google.com.8-8-8-8.sslip.io', 'sslip.io (accounts.google.com.8-8-8-8.sslip.io)'],
    ['login.okta.com.8.8.8.8.xip.io', 'xip.io (login.okta.com.8.8.8.8.xip.io)'],
    ['8.8.8.8.nip.io', 'nip.io (8.8.8.8.nip.io)'],
    ['nip.io', 'nip.io'],
    ['accounts.google.com.evilnip.io', 'evilnip.io (accounts.google.com.evilnip.io)'],
  ])('leads IP-embedding DNS %s with the suffix, the only part nobody picks', (provider, named) => {
    const d = providerDialog(provider, home)
    expect(d.message).toBe(`Continue to ${named} in this tab?`)
    expect(d.detail).toContain(`going to ${named},`)
    expect(d.detail).toContain(`with ${provider}:`)
  })

  it('says what a yes covers: this site with this provider (#2)', () => {
    const d = providerDialog('provider.example', home)
    expect(d.detail).toContain('A yes covers only box-1.term.example with provider.example')
    expect(d.detail).not.toContain('will not ask about this site again')
  })
})
