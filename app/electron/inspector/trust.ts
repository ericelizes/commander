// Unlisted sign-in providers the user accepted, kept in
// <userData>/trusted-sign-in-providers.json (a JSON list of {home, provider}
// pairs of exact hostnames; delete an entry, or the file, to be asked again).
// Read on every question, so windows cannot overwrite each other. Electron only
// as types: trust.test.ts runs it on a temp file.

import * as fs from 'node:fs'
import * as path from 'node:path'
import type { MessageBoxSyncOptions, WebContents } from 'electron'

import type { ProviderGate } from './guard'
import { hostOf, questionLead } from './policy'

type Pair = { home: string; provider: string }

export function trustFile(userData: string): string {
  return path.join(userData, 'trusted-sign-in-providers.json')
}

function isPair(p: unknown): p is Pair {
  const pair = p as Pair | null
  return typeof pair?.home === 'string' && typeof pair.provider === 'string'
}

// readTrusted: a missing or damaged file trusts nobody, so the user is asked. An
// entry that is no pair (the bare hostnames of the first version) trusts nobody
// either, and the next yes drops it.
function readTrusted(file: string): Pair[] {
  try {
    const saved: unknown = JSON.parse(fs.readFileSync(file, 'utf8'))
    return Array.isArray(saved) ? saved.filter(isPair) : []
  } catch {
    return []
  }
}

function writeTrusted(file: string, pairs: Pair[]): void {
  const tmp = `${file}.${process.pid}.tmp`
  fs.writeFileSync(tmp, JSON.stringify(pairs, null, 2) + '\n', { mode: 0o600 })
  fs.renameSync(tmp, file)
}

export type Ask = (provider: string, home: string, asker: WebContents) => boolean

// One question at a time across every window and tab: this is per process, not
// per gate, and each window has its own gate.
let asking = false

// providerGate trusts a home and provider pair in file and remembers a yes. A no
// is not remembered here: the guard remembers it on the page, and a misclick must
// not lock the provider out. A failed save still lets this sign-in go on; the
// user is asked again next time.
export function providerGate(file: string, ask: Ask): ProviderGate {
  return {
    trusted: (provider, home) => readTrusted(file).some((p) => p.provider === provider && p.home === hostOf(home)),
    ask: (provider, home, asker) => {
      if (asking) return 'busy'
      asking = true
      try {
        if (!ask(provider, home, asker)) return 'no'
      } finally {
        asking = false
      }
      try {
        writeTrusted(file, [...readTrusted(file), { home: hostOf(home), provider }])
      } catch (err) {
        console.warn(`could not save trusted sign-in provider: ${String(err)}`)
      }
      return 'yes'
    },
  }
}

export const CONTINUE_HERE = 1

export type DialogWindow = { isDestroyed: () => boolean }

// askInWindow is the Ask behind the real dialog. Its parent is the window that
// asked: a sign-in window owns its page, while a tab's view has no window of its
// own and asks in main. With no window left to ask in, the answer is no.
export function askInWindow<W extends DialogWindow>(main: W, windowOf: (wc: WebContents) => W | null, show: (parent: W, options: MessageBoxSyncOptions) => number): Ask {
  return (provider, home, asker) => {
    const owner = windowOf(asker)
    const parent = owner && !owner.isDestroyed() ? owner : main
    return !parent.isDestroyed() && show(parent, providerDialog(provider, home)) === CONTINUE_HERE
  }
}

// providerDialog is the question, in neutral words since a plain link cannot be
// told from a sign-in hand-off: the default and Esc send the page to the
// browser. It leads with questionLead (the registrable domain, or the suffix on
// IP-embedding DNS), since a long host can open with any labels its owner likes,
// then gives the exact host; URL hostnames are already punycode, so a look-alike
// reads as xn--.
export function providerDialog(provider: string, home: string): MessageBoxSyncOptions {
  const lead = questionLead(provider)
  const named = lead === provider ? provider : `${lead} (${provider})`
  const tab = hostOf(home) || home
  return {
    type: 'question',
    title: 'Continue here?',
    message: `Continue to ${named} in this tab?`,
    detail:
      `The ${tab} tab is going to ${named}, which Commander does not know as a sign-in provider.\n\n` +
      'Continue here only if you expected this site. Its pages then stay in this app, which shares one browser profile across its tabs. ' +
      `A yes covers only ${tab} with ${provider}: any other site or provider gets its own question.\n\n` +
      'Otherwise it opens in your browser.',
    buttons: ['Open in browser', 'Continue here'],
    defaultId: 0,
    cancelId: 0,
    noLink: true,
  }
}
