import { describe, expect, it } from 'vitest'
import { cmdrdDaemonArgs, daemonEnv, ensureDaemon, kickDaemon, readApiURL, watchDaemon, type DaemonDeps, type DaemonState } from './cmdrd'

interface Fake {
  deps: DaemonDeps
  runs: string[]
  spawned: string[]
  envs: Record<string, string>[]
}

// upAfterProbes: probe() answers true from that call on (-1: never).
function fake(platform: string, opts: { url?: string, upAfterProbes?: number, runFails?: boolean, holderAlive?: boolean } = {}): Fake {
  const runs: string[] = []
  const spawned: string[] = []
  const envs: Record<string, string>[] = []
  let probes = 0
  const deps: DaemonDeps = {
    platform,
    commanderHome: '/ch',
    home: '/tmp/example',
    uid: 501,
    readText: async (file) => {
      if (file === '/ch/api-url' && opts.url !== undefined) return opts.url
      throw new Error('ENOENT')
    },
    probe: async () => {
      const up = (opts.upAfterProbes ?? 0) >= 0 && probes >= (opts.upAfterProbes ?? 0)
      probes++
      return up
    },
    run: async (cmd, args) => {
      runs.push([cmd, ...args].join(' '))
      if (opts.runFails) throw new Error('no unit')
    },
    lockHolderAlive: async () => opts.holderAlive ?? false,
    spawnDetached: (cmd, args, env) => {
      spawned.push([cmd, ...args].join(' '))
      envs.push(env)
    },
    sleep: async () => {},
  }
  return { deps, runs, spawned, envs }
}

describe('readApiURL', () => {
  it('trims whitespace and trailing slashes', async () => {
    expect(await readApiURL(fake('darwin', { url: 'http://127.0.0.1:8787/\n' }).deps)).toBe('http://127.0.0.1:8787')
    expect(await readApiURL(fake('darwin').deps)).toBeUndefined()
  })
})

describe('kickDaemon', () => {
  it('macOS: launchctl kickstart of the user agent', async () => {
    const f = fake('darwin')
    await kickDaemon(f.deps)
    expect(f.runs).toEqual(['/bin/launchctl kickstart gui/501/org.commander'])
    expect(f.spawned).toEqual([])
  })

  it('Linux: systemctl --user start commander.service', async () => {
    const f = fake('linux')
    await kickDaemon(f.deps)
    expect(f.runs).toEqual(['systemctl --user start commander.service'])
  })

  it('macOS never starts its own cmdrd, even when kickstart fails', async () => {
    // install.sh between bootout and bootstrap: kickstart says the job is not loaded.
    const f = fake('darwin', { runFails: true })
    await kickDaemon(f.deps)
    expect(f.spawned).toEqual([])
  })

  it('Linux falls back to spawning ~/.local/bin/cmdrd, with the unit PATH', async () => {
    const f = fake('linux', { runFails: true })
    await kickDaemon(f.deps)
    expect(f.spawned).toEqual(['/tmp/example/.local/bin/cmdrd'])
    const dirs = f.envs[0].PATH.split(':')
    expect(dirs.slice(0, 4)).toEqual(['/tmp/example/.local/bin', '/tmp/example/.cargo/bin', '/opt/homebrew/bin', '/usr/local/bin'])
    expect(f.envs[0].COMMANDER_CHAT_HANDOVER).toBe('1')
  })
})

describe('daemonEnv', () => {
  it('puts the tool dirs ahead of a bare GUI PATH, without repeats', () => {
    expect(daemonEnv('/h', '/usr/bin:/bin:/usr/sbin:/sbin').PATH)
      .toBe('/h/.local/bin:/h/.cargo/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin')
  })
})

describe('watchDaemon', () => {
  it('macOS: cmdrd gone for a whole install (job booted out) never spawns one', async () => {
    let up = true
    let ticks = 0
    const f = fake('darwin', { url: 'http://127.0.0.1:8787', runFails: true })
    f.deps.probe = async () => up
    let stop = () => {}
    const done = new Promise<void>((resolve) => {
      f.deps.sleep = async () => {
        ticks++
        if (ticks === 2) up = false // bootout
        if (ticks === 80) up = true // bootstrap: launchd starts it
        if (ticks > 90) {
          stop()
          resolve()
        }
      }
    })
    const states: string[] = []
    stop = watchDaemon(f.deps, (s) => states.push(s.status), 5000)
    await done
    expect(f.runs.length).toBeGreaterThan(0)
    expect(f.spawned).toEqual([])
    expect(states.at(-1)).toBe('online')
  })
})

describe('ensureDaemon', () => {
  it('online right away without kicking', async () => {
    const f = fake('darwin', { url: 'http://127.0.0.1:8787' })
    const states: DaemonState[] = []
    const final = await ensureDaemon(f.deps, { onState: (s) => states.push(s) })
    expect(final).toEqual({ status: 'online', url: 'http://127.0.0.1:8787' })
    expect(states.map((s) => s.status)).toEqual(['checking', 'online'])
    expect(f.runs).toEqual([])
  })

  it('down: reports starting, kicks, then online', async () => {
    const f = fake('darwin', { url: 'http://127.0.0.1:8787', upAfterProbes: 3 })
    const states: DaemonState[] = []
    const final = await ensureDaemon(f.deps, { onState: (s) => states.push(s) })
    expect(final.status).toBe('online')
    expect(states.map((s) => s.status)).toEqual(['checking', 'starting', 'online'])
    expect(states[1].message).toBe('offline: starting cmdrd…')
    expect(f.runs).toHaveLength(1)
  })

  it('gives up as offline after the timeout', async () => {
    const f = fake('linux', { url: 'http://127.0.0.1:8787', upAfterProbes: -1 })
    const final = await ensureDaemon(f.deps, { timeoutMs: 2000, intervalMs: 500 })
    expect(final.status).toBe('offline')
  })

  it('no api-url yet counts as down', async () => {
    const f = fake('linux')
    const final = await ensureDaemon(f.deps, { timeoutMs: 1000, intervalMs: 500 })
    expect(final.status).toBe('offline')
    expect(f.runs).toEqual(['systemctl --user start commander.service'])
  })
})

describe('one cmdrd: a live lock holder is never doubled', () => {
  it('does not kick or spawn while the lock holder is alive but slow', async () => {
    const f = fake('linux', { url: 'http://127.0.0.1:8787', upAfterProbes: 4, runFails: true, holderAlive: true })
    const states: DaemonState[] = []
    const final = await ensureDaemon(f.deps, { onState: (s) => states.push(s) })
    expect(final.status).toBe('online')
    expect(f.runs).toEqual([])
    expect(f.spawned).toEqual([])
    expect(states[1].message).toBe('cmdrd is running but slow to answer; waiting…')
  })

  it('says the running cmdrd is not answering instead of claiming it did not start', async () => {
    const f = fake('darwin', { url: 'http://127.0.0.1:8787', upAfterProbes: -1, holderAlive: true })
    const final = await ensureDaemon(f.deps, { timeoutMs: 1000, intervalMs: 500 })
    expect(final).toEqual({ status: 'offline', message: 'cmdrd is running but not answering; see ~/.commander/logs/cmdrd.err' })
    expect(f.runs).toEqual([])
  })

  it('spawns when cmdrd is dead (no lock holder) at app start', async () => {
    const f = fake('linux', { url: 'http://127.0.0.1:8787', upAfterProbes: 3, runFails: true, holderAlive: false })
    const final = await ensureDaemon(f.deps)
    expect(final.status).toBe('online')
    expect(f.spawned).toEqual(['/tmp/example/.local/bin/cmdrd'])
  })

  it('kicks once the slow holder dies mid-wait (crash)', async () => {
    let alive = true
    const f = fake('linux', { url: 'http://127.0.0.1:8787', upAfterProbes: 6, runFails: true })
    f.deps.lockHolderAlive = async () => alive
    let ticks = 0
    f.deps.sleep = async () => {
      if (++ticks === 2) alive = false
    }
    const final = await ensureDaemon(f.deps)
    expect(final.status).toBe('online')
    expect(f.spawned).toEqual(['/tmp/example/.local/bin/cmdrd'])
  })

  it('after a wake, misses with a live holder back off and never spawn', async () => {
    let up = true
    let ticks = 0
    const delays: number[] = []
    const f = fake('linux', { url: 'http://127.0.0.1:8787', runFails: true, holderAlive: true })
    f.deps.probe = async () => up
    let stop = () => {}
    const done = new Promise<void>((resolve) => {
      f.deps.sleep = async (ms) => {
        delays.push(ms)
        ticks++
        if (ticks === 2) up = false // laptop wakes: the daemon is slow for a while
        if (ticks === 8) up = true
        if (ticks > 10) {
          stop()
          resolve()
        }
      }
    })
    const states: string[] = []
    stop = watchDaemon(f.deps, (s) => states.push(s.status), 5000, 30000)
    await done
    expect(f.runs).toEqual([])
    expect(f.spawned).toEqual([])
    expect(delays.slice(2, 7)).toEqual([10000, 20000, 30000, 30000, 30000])
    expect(delays.at(-1)).toBe(5000)
    expect(states).toEqual(['checking', 'online'])
  })

  it('watch: a crashed daemon (no holder) is restarted', async () => {
    let up = true
    let ticks = 0
    const f = fake('linux', { url: 'http://127.0.0.1:8787', runFails: true, holderAlive: false })
    f.deps.probe = async () => up
    let stop = () => {}
    const done = new Promise<void>((resolve) => {
      f.deps.sleep = async () => {
        ticks++
        if (ticks === 2) up = false
        if (ticks === 4) up = true
        if (ticks > 6) {
          stop()
          resolve()
        }
      }
    })
    stop = watchDaemon(f.deps, () => {}, 5000)
    await done
    expect(f.spawned).toEqual(['/tmp/example/.local/bin/cmdrd'])
  })

  it('cmdrdDaemonArgs matches the daemon, not chat hosts or other programs', () => {
    expect(cmdrdDaemonArgs('/opt/x/.local/bin/cmdrd\n')).toBe(true)
    expect(cmdrdDaemonArgs('cmdrd --notifications=false')).toBe(true)
    expect(cmdrdDaemonArgs('/x/bin/cmdrd.prev')).toBe(true)
    expect(cmdrdDaemonArgs('/x/bin/cmdrd __chat-host /tmp/cfg')).toBe(false)
    expect(cmdrdDaemonArgs('/usr/bin/vim')).toBe(false)
    expect(cmdrdDaemonArgs('')).toBe(false)
  })
})
