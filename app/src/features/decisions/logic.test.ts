import { describe, expect, it } from 'vitest'
import { fakeApi, inputCard, optionsCard, plainFlag, stepsCard } from './fixtures'
import { answerMode, cmdError, formatAge, readyChoice, resolveArgv, resolveNeeds, stillPending, validationHint } from './logic'

describe('answer modes and validation', () => {
  it('matches the TUI: options need a pick, steps are done, input needs text', () => {
    expect(answerMode(optionsCard.decision!)).toBe('options')
    expect(answerMode(stepsCard.decision!)).toBe('steps')
    expect(answerMode(inputCard.decision!)).toBe('input')

    expect(readyChoice(optionsCard.decision!, -1, 'a note')).toBeNull()
    expect(readyChoice(optionsCard.decision!, 1, '')).toBe('Kafka topic + consumer')
    expect(readyChoice(optionsCard.decision!, 7, '')).toBeNull()
    expect(readyChoice(stepsCard.decision!, -1, '')).toBe('done')
    expect(readyChoice(inputCard.decision!, -1, '   ')).toBeNull()
    expect(readyChoice(inputCard.decision!, -1, 'example-app Open')).toBe('answered')
    expect(validationHint(optionsCard.decision!)).toBe('Pick an option (1–3)')
  })
})

describe('resolveArgv', () => {
  it('builds the internal-resolve-needs argv', () => {
    expect(resolveArgv(optionsCard, 'decide', 'Postgres outbox table', '  ship it ')).toEqual([
      'internal-resolve-needs', '--project', 'imports', '--bead', 'imports-12.3', '--action', 'decide',
      '--choice', 'Postgres outbox table', '--text', 'ship it',
    ])
    expect(resolveArgv(stepsCard, 'decide', 'done', '')).toEqual([
      'internal-resolve-needs', '--project', 'imports', '--bead', 'imports-14.1', '--action', 'decide', '--choice', 'done',
    ])
    expect(resolveArgv(plainFlag, 'approve', 'ignored', 'ignored')).toEqual([
      'internal-resolve-needs', '--project', 'commander', '--bead', 'cmdr-31', '--action', 'approve',
    ])
  })
})

it('binds an answer to the displayed planning revision', () => {
 expect(resolveArgv({...plainFlag, plan_revision: 42}, 'approve')).toContain('--revision')
 expect(resolveArgv({...plainFlag, plan_revision: 42}, 'approve')).toContain('42')
})

describe('cmdError', () => {
  it('prefers the JSON error cmdr prints on stderr', () => {
    expect(cmdError({ ok: false, stdout: '', stderr: '{"error":"bead not found"}\n', exit: 1 })).toBe('bead not found')
    expect(cmdError({ ok: false, stdout: '', stderr: 'warning: x\nplain failure', exit: 1 })).toBe('warning: x\nplain failure')
    expect(cmdError({ ok: false, stdout: '', stderr: '', exit: -1, error: 'command not allowed' })).toBe('command not allowed')
    expect(cmdError({ ok: false, stdout: '', stderr: '', exit: 3 })).toBe('cmdr exited with status 3')
  })
})

describe('formatAge', () => {
  it('is compact and empty when unknown', () => {
    const now = 1_000_000 * 1000
    expect(formatAge(undefined, now)).toBe('')
    expect(formatAge(1_000_000 - 20, now)).toBe('now')
    expect(formatAge(1_000_000 - 600, now)).toBe('10m')
    expect(formatAge(1_000_000 - 7200, now)).toBe('2h')
    expect(formatAge(1_000_000 - 3 * 86400, now)).toBe('3d')
  })
})

describe('resolveNeeds', () => {
  it('runs the command while the flag is open', async () => {
    const api = fakeApi()
    expect(await resolveNeeds(api, optionsCard, 'decide', 'Kafka topic + consumer')).toEqual({ status: 'ok' })
    expect(api.calls).toHaveLength(1)
    expect(stillPending(await api.snapshot(), optionsCard)).toBe(false)
  })

  it('resumes a saved answer when the flag is absent from the snapshot', async () => {
    const api = fakeApi([stepsCard])
    expect(await resolveNeeds(api, optionsCard, 'decide', 'x')).toEqual({ status: 'gone' })
    expect(api.calls).toHaveLength(1)
    expect(api.calls[0]).toContain('--resume-only')
  })

  it('reports resume failures so cleanup can be retried', async () => {
    const api = fakeApi([])
    api.next = { ok: false, exit: 1, stderr: 'queue busy' }
    expect(await resolveNeeds(api, optionsCard, 'decide', 'x')).toEqual({ status: 'error', message: 'queue busy' })
    expect(api.calls[0]).toContain('--resume-only')
  })

  it('still runs when the snapshot is unavailable, and reports failures', async () => {
    const api = fakeApi()
    api.snapshot = async () => { throw new Error('offline') }
    api.next = new Error('network down')
    expect(await resolveNeeds(api, plainFlag, 'defer')).toEqual({ status: 'error', message: 'network down' })
    expect(api.calls).toHaveLength(1)
  })
})
