import { act, cleanup, render, screen } from '@testing-library/react'
import { JSDOM } from 'jsdom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { BRIDGE_TAG, buildSrcdoc, handleBridgeRequest, parseRequest } from './bridge'
import type { BridgeHandlers } from './bridge'
import decisionForm from './examples/decision-form.html?raw'
import statusDashboard from './examples/status-dashboard.html?raw'
import { fakeApi, optionsCard } from './fixtures'
import { HtmlCardHost } from './HtmlCardHost'
import type { NeedsYouItem } from './types'

afterEach(cleanup)

const pricing: NeedsYouItem = {
  project: 'example-app', bead_id: 'example-app-9', title: 'Pricing', workstream: 'example-app pricing', rank: 1,
  decision: { question: 'Which pricing tier ships first?', options: [{ label: 'Starter' }, { label: 'Team' }, { label: 'Enterprise' }] },
}

// Sends a bridge request as if from the iframe and returns what the host replied.
async function fromFrame(frame: HTMLIFrameElement, data: unknown, source: unknown = frame.contentWindow) {
  const post = vi.spyOn(frame.contentWindow!, 'postMessage')
  await act(async () => {
    window.dispatchEvent(new MessageEvent('message', { data, source: source as Window }))
    await new Promise(r => setTimeout(r, 0))
  })
  const reply = post.mock.calls.at(-1)?.[0]
  post.mockRestore()
  return reply
}

describe('HtmlCardHost sandbox', () => {
  it('renders a static page with no script, no same-origin and a strict CSP', () => {
    render(<HtmlCardHost html={statusDashboard} title="Import status" api={fakeApi()} />)
    const frame = screen.getByTitle('Import status') as HTMLIFrameElement
    expect(frame.getAttribute('sandbox')).toBe('')
    const doc = frame.getAttribute('srcdoc')!
    expect(doc).toContain("script-src 'none'")
    expect(doc).toContain("connect-src 'none'")
    expect(doc).not.toContain('window.cmdr')
    // The CSP comes first in <head>, before the page's own markup.
    expect(doc.indexOf('Content-Security-Policy')).toBeLessThan(doc.indexOf('<title>'))
    expect(doc.startsWith('<!doctype html>')).toBe(true)
  })

  it('allows scripts only when asked, never same-origin or navigation', () => {
    render(<HtmlCardHost html={decisionForm} title="Pricing" api={fakeApi()} allowScripts />)
    const frame = screen.getByTitle('Pricing') as HTMLIFrameElement
    const sandbox = frame.getAttribute('sandbox')!
    expect(sandbox).toBe('allow-scripts')
    for (const bad of ['allow-same-origin', 'allow-top-navigation', 'allow-popups', 'allow-forms']) {
      expect(sandbox).not.toContain(bad)
    }
    expect(frame.getAttribute('srcdoc')).toContain("script-src 'unsafe-inline'")
    expect(frame.getAttribute('srcdoc')).toContain('window.cmdr=Object.freeze')
  })

  it('injects into pages without a head', () => {
    expect(buildSrcdoc('<p>hi</p>', false)).toMatch(/^<!doctype html><head><meta http-equiv="Content-Security-Policy"[^>]+><\/head><p>hi<\/p>$/)
    expect(buildSrcdoc('<html><body>x</body></html>', false)).toMatch(/^<html><head><meta /)
    expect(buildSrcdoc('<!DOCTYPE html><header>x</header>', false)).toMatch(/^<!DOCTYPE html><head><meta /)
  })
})

describe('HtmlCardHost bridge', () => {
  it('submits a decision from the frame for its own flag', async () => {
    const api = fakeApi([pricing])
    const onDone = vi.fn()
    render(<HtmlCardHost html={decisionForm} title="Pricing" api={api} allowScripts needs={pricing} onDone={onDone} />)
    const frame = screen.getByTitle('Pricing') as HTMLIFrameElement
    const reply = await fromFrame(frame, { [BRIDGE_TAG]: 1, id: 1, method: 'submitDecision', params: { choice: 'Team', text: 'start small' } })
    expect(reply).toEqual({ [BRIDGE_TAG]: 1, id: 1, ok: true })
    expect(api.calls).toEqual([['internal-resolve-needs', '--project', 'example-app', '--bead', 'example-app-9', '--action', 'decide', '--choice', 'Team', '--text', 'start small']])
    expect(onDone).toHaveBeenCalledWith({ kind: 'answered', action: 'decide', choice: 'Team', note: 'start small' })
  })

  it('refuses choices that are not on the card, and pages without a flag', async () => {
    const api = fakeApi([pricing])
    const { unmount } = render(<HtmlCardHost html={decisionForm} title="Pricing" api={api} allowScripts needs={pricing} />)
    let frame = screen.getByTitle('Pricing') as HTMLIFrameElement
    const reply = await fromFrame(frame, { [BRIDGE_TAG]: 1, id: 2, method: 'submitDecision', params: { choice: 'Free forever' } })
    expect(reply).toMatchObject({ ok: false, error: '"Free forever" is not one of the card\'s options' })
    unmount()

    render(<HtmlCardHost html={decisionForm} title="Loose" api={api} allowScripts />)
    frame = screen.getByTitle('Loose') as HTMLIFrameElement
    expect(await fromFrame(frame, { [BRIDGE_TAG]: 1, id: 3, method: 'submitDecision', params: { choice: 'Team' } }))
      .toMatchObject({ ok: false, error: 'this page is not attached to a decision' })
    expect(api.calls).toHaveLength(0)
  })

  it('reports a flag answered elsewhere', async () => {
    const api = fakeApi([])
    const onDone = vi.fn()
    render(<HtmlCardHost html={decisionForm} title="Pricing" api={api} allowScripts needs={pricing} onDone={onDone} />)
    const frame = screen.getByTitle('Pricing') as HTMLIFrameElement
    expect(await fromFrame(frame, { [BRIDGE_TAG]: 1, id: 4, method: 'submitDecision', params: { choice: 'Team' } }))
      .toMatchObject({ ok: false, error: 'already answered elsewhere' })
    expect(onDone).toHaveBeenCalledWith({ kind: 'gone' })
  })

  it('copies and opens only http(s) URLs', async () => {
    const api = fakeApi()
    render(<HtmlCardHost html={decisionForm} title="Pricing" api={api} allowScripts needs={optionsCard} />)
    const frame = screen.getByTitle('Pricing') as HTMLIFrameElement
    expect(await fromFrame(frame, { [BRIDGE_TAG]: 1, id: 5, method: 'copy', params: { text: 'Pricing tier: Team' } })).toMatchObject({ ok: true })
    expect(api.copied).toEqual(['Pricing tier: Team'])
    expect(await fromFrame(frame, { [BRIDGE_TAG]: 1, id: 6, method: 'openUrl', params: { url: 'https://www.notion.so/Pricing-spec-123' } })).toMatchObject({ ok: true })
    expect(await fromFrame(frame, { [BRIDGE_TAG]: 1, id: 7, method: 'openUrl', params: { url: 'javascript:alert(1)' } })).toMatchObject({ ok: false })
    expect(await fromFrame(frame, { [BRIDGE_TAG]: 1, id: 8, method: 'openUrl', params: { url: 'file:///etc/passwd' } })).toMatchObject({ ok: false })
    expect(api.opened).toEqual(['https://www.notion.so/Pricing-spec-123'])
  })

  it('ignores messages from anywhere but its own frame, and unknown methods', async () => {
    const api = fakeApi([pricing])
    render(<HtmlCardHost html={decisionForm} title="Pricing" api={api} allowScripts needs={pricing} />)
    const frame = screen.getByTitle('Pricing') as HTMLIFrameElement
    const req = { [BRIDGE_TAG]: 1, id: 9, method: 'submitDecision', params: { choice: 'Team' } }
    expect(await fromFrame(frame, req, window)).toBeUndefined()
    expect(await fromFrame(frame, { [BRIDGE_TAG]: 1, id: 10, method: 'runCmd', params: { argv: ['tab', 'rm'] } })).toBeUndefined()
    expect(api.calls).toHaveLength(0)
  })

  it('does not listen at all when scripts are off', async () => {
    const api = fakeApi([pricing])
    render(<HtmlCardHost html={statusDashboard} title="Static" api={api} needs={pricing} />)
    const frame = screen.getByTitle('Static') as HTMLIFrameElement
    expect(await fromFrame(frame, { [BRIDGE_TAG]: 1, id: 11, method: 'submitDecision', params: { choice: 'Team' } })).toBeUndefined()
    expect(api.calls).toHaveLength(0)
  })
})

describe('bridge validation', () => {
  const h: BridgeHandlers = { submitDecision: async () => {}, copy: async () => {}, openUrl: () => {} }
  it('parses only tagged, known requests', () => {
    expect(parseRequest(null)).toBeNull()
    expect(parseRequest({ id: 1, method: 'copy' })).toBeNull()
    expect(parseRequest({ [BRIDGE_TAG]: 1, id: {}, method: 'copy' })).toBeNull()
    expect(parseRequest({ [BRIDGE_TAG]: 1, id: 1, method: 'copy' })).toEqual({ id: 1, method: 'copy', params: {} })
  })
  it('rejects empty or oversized answers', async () => {
    expect(await handleBridgeRequest({ id: 1, method: 'submitDecision', params: {} }, h)).toMatchObject({ ok: false })
    expect(await handleBridgeRequest({ id: 1, method: 'submitDecision', params: { text: 'x'.repeat(5000) } }, h)).toMatchObject({ ok: false, error: 'answer too long' })
    expect(await handleBridgeRequest({ id: 1, method: 'copy', params: { text: 3 } }, h)).toMatchObject({ ok: false })
  })
})

describe('example pages', () => {
  it('the decision form calls window.cmdr.submitDecision with the picked tier and note', async () => {
    const calls: unknown[] = []
    const dom = new JSDOM(decisionForm, {
      runScripts: 'dangerously',
      beforeParse(win) {
        (win as unknown as { cmdr: unknown }).cmdr = {
          submitDecision: async (a: unknown) => { calls.push(a) },
          copy: async (t: string) => { calls.push({ copy: t }) },
          openUrl: () => {},
        }
      },
    })
    const doc = dom.window.document
    const submit = doc.getElementById('submit') as HTMLButtonElement
    expect(submit.disabled).toBe(true)
    ;(doc.querySelector('[data-choice="Team"]') as HTMLButtonElement).click()
    expect(submit.disabled).toBe(false)
    ;(doc.getElementById('note') as HTMLTextAreaElement).value = 'start small'
    submit.click()
    await new Promise(r => setTimeout(r, 0))
    expect(calls).toEqual([{ choice: 'Team', text: 'start small' }])
    expect(doc.getElementById('status')!.textContent).toBe('✓ Sent to the Manager')
    ;(doc.getElementById('copy') as HTMLButtonElement).click()
    expect(calls[1]).toEqual({ copy: 'Pricing tier: Team' })
    dom.window.close()
  })

  it('the status dashboard is static (no scripts needed)', () => {
    expect(statusDashboard).not.toMatch(/<script/i)
    expect(decisionForm).toMatch(/window\.cmdr\.submitDecision/)
  })
})
