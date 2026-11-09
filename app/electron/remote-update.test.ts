import { EventEmitter } from 'node:events'
import { mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { describe, expect, it, vi } from 'vitest'
import {
  confirmText,
  createRemoteUpdater,
  registerRemoteUpdateIpc,
  remoteCommand,
  remoteSettings,
  runSsh,
  shQuote,
  sshArgs,
  sshTarget,
  type RemoteUpdateDeps,
  type RemoteUpdateStatus,
} from './remote-update'
import * as ch from './shell-channels'
import { shellApi } from './shell-preload'

const main = 'abcdef0123456789abcdef0123456789abcdef01'
const status: RemoteUpdateStatus = {
  host: 'build-box',
  repo: '/srv/code/commander',
  main,
  installed: '1234567aaaa',
  behind: 2,
  dirty: false,
  can_rollback: true,
  commits: [
    { hash: 'abcdef0aaaa', subject: 'feat: one', review: 'reviewed' },
    { hash: '9999999bbbb', subject: 'fix: two‮ evil', review: 'not confirmed' },
  ],
}

describe('remote settings and target', () => {
  it('reads the target from the env, else the files in COMMANDER_HOME', () => {
    const home = mkdtempSync(join(tmpdir(), 'cmdr-remote-'))
    expect(remoteSettings(home, {})).toEqual({ ssh: undefined, sshSuffix: undefined })
    writeFileSync(join(home, 'remote-ssh'), 'box.example.net\n')
    writeFileSync(join(home, 'remote-ssh-suffix'), '.example.net\n')
    expect(remoteSettings(home, {})).toEqual({ ssh: 'box.example.net', sshSuffix: '.example.net' })
    expect(remoteSettings(home, { COMMANDER_REMOTE_SSH: 'other' }).ssh).toBe('other')
  })

  it('prefers the explicit target, else host + suffix, else says how to configure', () => {
    expect(sshTarget({ ssh: 'me@box', sshSuffix: '.x.net' }, 'build-box')).toEqual({ target: 'me@box' })
    expect(sshTarget({ sshSuffix: '.x.net' }, 'build-box')).toEqual({ target: 'build-box.x.net' })
    expect(sshTarget({}, 'build-box')).toEqual({ error: expect.stringContaining('remote-ssh') })
  })

  it('refuses targets ssh would read as options or a shell would interpret', () => {
    for (const bad of ['-oProxyCommand=x', 'a b', 'a;b', '$(x)', 'a@-b']) expect(sshTarget({ ssh: bad }, 'h')).toHaveProperty('error')
    expect(sshTarget({ sshSuffix: '.x.net' }, '-oProxyCommand=x')).toHaveProperty('error')
    expect(sshTarget({ sshSuffix: 'x.net;rm' }, 'h')).toHaveProperty('error')
  })
})

describe('remote command', () => {
  it('pins the install to the confirmed main and quotes the repo', () => {
    const r = remoteCommand('install', "/srv/it's here", main)
    if (!('command' in r)) throw new Error(r.error)
    expect(r.command).toContain('export PATH=$HOME/.local/bin:$PATH XDG_RUNTIME_DIR=/run/user/$(id -u);')
    expect(r.command).toContain(`cd ${shQuote("/srv/it's here")} &&`)
    expect(r.command).toContain(`test "$(git rev-parse HEAD)" = ${main}`)
    expect(r.command).toMatch(/&& just build && cmdr install --yes --restart-cmdrd$/)
    expect(shQuote("it's")).toBe(`'it'\\''s'`)
  })

  it('rolls back with the rollback verb', () => {
    expect(remoteCommand('rollback', '/srv/c', '')).toEqual({ command: expect.stringMatching(/cd '\/srv\/c' && cmdr install --rollback --yes --restart-cmdrd$/) })
  })

  it('refuses a missing or relative repo and a short main', () => {
    expect(remoteCommand('install', undefined, main)).toHaveProperty('error')
    expect(remoteCommand('install', 'rel/path', main)).toHaveProperty('error')
    expect(remoteCommand('install', '/a\nb', main)).toHaveProperty('error')
    expect(remoteCommand('install', '/a', 'abc')).toHaveProperty('error')
  })

  it('passes the target after -- so it is never an option', () => {
    expect(sshArgs('box', 'cmd')).toEqual(['-o', 'BatchMode=yes', '-o', 'ConnectTimeout=15', '--', 'box', 'cmd'])
  })

  it('the dialog names the revision, host, commits and the command, without control runes', () => {
    const t = confirmText('install', status, 'build-box.x.net')
    expect(t.message).toBe('Install abcdef0 on build-box?')
    expect(t.detail).toContain('abcdef0 feat: one (reviewed)')
    expect(t.detail).toContain('9999999 fix: two evil (not confirmed)')
    expect(t.detail).not.toContain('‮')
    expect(t.detail).toContain('build-box.x.net')
    expect(confirmText('rollback', status, 't').message).toBe('Roll back Commander on build-box?')
  })
})

function deps(over: Partial<RemoteUpdateDeps> = {}) {
  const d = {
    fetchStatus: vi.fn(async () => status),
    settings: () => ({ sshSuffix: '.x.net' }),
    confirm: vi.fn(async () => true),
    run: vi.fn(async (_args: string[], onLine: (l: string) => void) => {
      onLine('building')
      onLine('installed')
      return { code: 0 as number | null }
    }),
    ...over,
  }
  return d
}

describe('createRemoteUpdater', () => {
  it('runs nothing unless the person presses Install', async () => {
    const d = deps({ confirm: vi.fn(async () => false) })
    const res = await createRemoteUpdater(d)('install', () => {})
    expect(res).toEqual({ ok: false, declined: true })
    expect(d.run).not.toHaveBeenCalled()
  })

  it('confirms, then runs the install over SSH and streams its lines', async () => {
    const d = deps()
    const lines: string[] = []
    const res = await createRemoteUpdater(d)('install', (l) => lines.push(l))
    expect(res.ok).toBe(true)
    expect(d.confirm).toHaveBeenCalledWith(undefined, 'Install abcdef0 on build-box?', expect.any(String))
    const args = vi.mocked(d.run).mock.calls[0][0]
    expect(args.slice(0, 6)).toEqual(['-o', 'BatchMode=yes', '-o', 'ConnectTimeout=15', '--', 'build-box.x.net'])
    expect(args[6]).toContain('just build && cmdr install --yes --restart-cmdrd')
    expect(lines).toEqual(['building', 'installed'])
  })

  it('reports a failure with the output', async () => {
    const d = deps({ run: vi.fn(async (_a: string[], onLine: (l: string) => void) => (onLine('go: build failed'), { code: 1 })) })
    const res = await createRemoteUpdater(d)('install', () => {})
    expect(res).toEqual({ ok: false, error: 'the install over SSH exited with code 1', output: ['go: build failed'] })
  })

  it('accepts only install and rollback, and refuses before any dialog', async () => {
    const d = deps()
    const up = createRemoteUpdater(d)
    expect((await up('rm -rf /', () => {})).error).toMatch(/install or rollback/)
    expect((await createRemoteUpdater(deps({ settings: () => ({}) }))('install', () => {})).error).toMatch(/remote-ssh/)
    expect((await createRemoteUpdater(deps({ fetchStatus: vi.fn(async () => ({ ...status, behind: 0 })) }))('install', () => {})).error).toBe('already up to date')
    expect((await createRemoteUpdater(deps({ fetchStatus: vi.fn(async () => ({ ...status, dirty: true })) }))('install', () => {})).error).toMatch(/uncommitted/)
    expect((await createRemoteUpdater(deps({ fetchStatus: vi.fn(async () => { throw new Error('down') }) }))('install', () => {})).error).toMatch(/not reachable/)
    expect(d.confirm).not.toHaveBeenCalled()
  })

  it('shows at most one dialog a minute and one install at a time', async () => {
    let t = 0
    let release!: () => void
    const d = deps({ now: () => t, run: vi.fn(() => new Promise<{ code: number | null }>((r) => { release = () => r({ code: 0 }) })) })
    const up = createRemoteUpdater(d)
    const first = up('install', () => {})
    await vi.waitFor(() => expect(d.run).toHaveBeenCalled())
    expect((await up('rollback', () => {})).error).toMatch(/already running/)
    release()
    await first
    expect((await up('install', () => {})).error).toMatch(/just shown/)
    t += 61_000
    const again = up('install', () => {})
    await vi.waitFor(() => expect(d.run).toHaveBeenCalledTimes(2))
    release()
    expect((await again).ok).toBe(true)
  })

  it('registers one invoke channel that streams lines back to the caller', async () => {
    let handler!: (e: unknown, action: unknown) => Promise<unknown>
    const ipc = { handle: vi.fn((c: string, fn: typeof handler) => { if (c === ch.UPDATE_REMOTE_RUN) handler = fn }) }
    registerRemoteUpdateIpc(ipc as never, deps())
    const sender = { isDestroyed: () => false, send: vi.fn() }
    expect(await handler({ sender }, 'install')).toMatchObject({ ok: true })
    expect(sender.send).toHaveBeenCalledWith(ch.UPDATE_REMOTE_LINE, 'building')
    expect(ipc.handle).toHaveBeenCalledTimes(1)
  })
})

describe('runSsh', () => {
  it('runs /usr/bin/ssh with the args, no shell, and splits output into lines', async () => {
    const child = Object.assign(new EventEmitter(), { stdout: new EventEmitter(), stderr: new EventEmitter(), kill: vi.fn() })
    const spawnFn = vi.fn(() => child)
    const lines: string[] = []
    const done = runSsh(['--', 'box', 'cmd'], (l) => lines.push(l), spawnFn as never)
    child.stdout.emit('data', Buffer.from('one\ntw'))
    child.stdout.emit('data', Buffer.from('o\n'))
    child.stderr.emit('data', Buffer.from('warn\n'))
    child.emit('close', 0, null)
    expect(await done).toEqual({ code: 0 })
    expect(spawnFn).toHaveBeenCalledWith('/usr/bin/ssh', ['--', 'box', 'cmd'], { stdio: ['ignore', 'pipe', 'pipe'] })
    expect(lines).toEqual(['one', 'two', 'warn'])
  })
})

describe('preload runUpdate', () => {
  it('exposes only install/rollback and streams lines for the call', async () => {
    const listeners: Record<string, (e: unknown, l: string) => void> = {}
    const ipc = {
      on: vi.fn((c: string, fn: (e: unknown, l: string) => void) => { listeners[c] = fn }),
      removeListener: vi.fn((c: string) => { delete listeners[c] }),
      send: vi.fn(),
      invoke: vi.fn(async (_c: string, _a: string) => {
        listeners[ch.UPDATE_REMOTE_LINE]?.({}, 'line 1')
        return { ok: true }
      }),
    }
    const lines: string[] = []
    expect(await shellApi(ipc as never).runUpdate('sh -c x' as never, (l) => lines.push(l))).toEqual({ ok: true })
    expect(ipc.invoke).toHaveBeenCalledWith(ch.UPDATE_REMOTE_RUN, 'install')
    expect(lines).toEqual(['line 1'])
    expect(listeners[ch.UPDATE_REMOTE_LINE]).toBeUndefined()
  })
})
