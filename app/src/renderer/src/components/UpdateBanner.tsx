import { Download } from 'lucide-react'
import { BoatSketch } from './BoatSketch'
import { useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { useStore } from '../store'
import { appShell, bannerText, installRoute, jobActive, showBanner, startAppInstall, startUpdate, useUpdatePoll, useUpdateStore, type AppInstall, type UpdateStatus } from '../updateApi'

// A small pill in the rail: a download icon and how many commits
// main is ahead, hidden when up to date. It opens the panel with the commits,
// their review markers and the Install button (desktop app) or the terminal
// commands (plain web page). When cmdrd cannot show its confirmation (headless
// or another machine), Install goes to the desktop app's own dialog and SSH run.
export function UpdateBanner() {
  useUpdatePoll()
  const status = useUpdateStore((s) => s.status)
  const open = useUpdateStore((s) => s.panelOpen)
  const setOpen = useUpdateStore((s) => s.setPanelOpen)
  const appRun = useUpdateStore((s) => s.appInstall)
  // A finished app install stays until the panel closes, also once nothing is behind.
  if (!status || (!showBanner(status) && !appRun)) return null
  const failed = status.job?.state === 'failed' || (!!appRun && !appRun.running && !appRun.ok && !appRun.declined)
  const label = appRun?.running ? (appRun.action === 'rollback' ? 'Rolling back…' : 'Installing…') : bannerText(status)
  return (
    <>
      <button type="button" className={`rail-dot side-update${failed ? ' failed' : ''}`} onClick={() => setOpen(true)} aria-label={label} title={label} aria-haspopup="dialog">
        <Download size={14} strokeWidth={2.2} />
        {jobActive(status.job) || appRun?.running ? <span className="nav-update-dot" aria-hidden /> : null}
      </button>
      {/* A portal: a glass backdrop-filter would otherwise contain the fixed backdrop. */}
      {open &&
        createPortal(
          <UpdatePanel
            status={status}
            onClose={() => {
              setOpen(false)
              if (appRun && !appRun.running) useUpdateStore.setState({ appInstall: null })
            }}
          />,
          document.body,
        )}
    </>
  )
}

export function UpdatePanel({ status, onClose }: { status: UpdateStatus; onClose: () => void }) {
  const [error, setError] = useState('')
  const config = useStore((s) => s.config)
  const [asking, setAsking] = useState(false)
  const appRun = useUpdateStore((s) => s.appInstall)
  const route = installRoute(status, config?.localHost, !!appShell())
  const busy = jobActive(status.job) || asking || !!appRun?.running
  const act = async (action: 'install') => {
    if (!config) return
    setError('')
    if (route === 'app') {
      await startAppInstall(config, action)
      return
    }
    setAsking(true)
    const res = await startUpdate(config, action)
    setAsking(false)
    if (!res.ok) setError(res.error ?? 'could not start')
  }
  const seen = updatePhase(status, appRun, busy)
  // An update this dialog watched run ends as done once nothing is behind, even
  // when the restarted service no longer remembers the job.
  const watched = useRef(false)
  if (seen === 'working' || seen === 'confirming') watched.current = true
  const phase: Phase = watched.current && seen === 'ready' && status.behind === 0 ? 'done' : seen
  // While Commander restarts the dialog grows to fill the window and keeps its
  // boat (the start screen stays out of the way), then shrinks back.
  const reconnecting = useStore((s) => !!s.snapshot && s.conn !== 'live')
  const restarting = reconnecting && phase !== 'ready' && phase !== 'failed'
  const sailing = phase === 'working' || restarting
  const { notes, hidden } = changelog(status.commits)
  const details = route === 'app' && appRun ? appRun.lines.slice(-20) : (status.job?.events ?? []).slice(-20).map((e) => `${e.step} ${e.text}`)
  return (
    <div className={`modal-backdrop update-backdrop${restarting ? ' update-full' : ''}`} onMouseDown={(e) => e.target === e.currentTarget && !restarting && onClose()}>
      <div className={`modal update-panel${sailing || phase === 'done' ? ' update-tall' : ''}`} role="dialog" aria-modal="true" aria-label="Update">
        {!sailing && phase !== 'done' && <h3>{PHASE_TITLE[phase]}</h3>}
        <div className="modal-body">
          {phase === 'ready' && (
            <>
              <p className="update-sum">What's new</p>
              <ul className="update-notes">
                {notes.map((n, i) => (
                  <li key={i}>{n}</li>
                ))}
                {hidden > 0 && <li className="update-quiet">{notes.length ? `Plus ${hidden} smaller improvements` : `${hidden} smaller improvements`}</li>}
              </ul>
            </>
          )}
          {phase === 'confirming' && <p className="update-sum">Click Install in the window that just opened.</p>}
          {(sailing || phase === 'done') && (
            <div className="update-center">
              <BoatSketch arrived={!sailing} />
              <p className="update-headline">{restarting ? 'Restarting…' : sailing ? 'Updating…' : "You're up to date"}</p>
              <p className="update-sum">
                {restarting ? 'Back in a moment. Your chats keep running.' : sailing ? 'This takes a minute or two. Your chats keep running.' : 'Commander is on the newest version.'}
              </p>
            </div>
          )}
          {phase === 'failed' && <p className="update-sum">Nothing changed{failReason(appRun?.error ?? status.job?.error)}</p>}
          {status.dirty && phase === 'ready' && <p className="update-error">Can't update yet: there are unsaved code changes on this machine.</p>}
          {error && (
            <p className="update-error" role="alert">
              {error}
            </p>
          )}
          {phase === 'failed' && details.length > 0 && (
            <details className="update-web">
              <summary>Details</summary>
              <pre>{[appRun?.error ?? status.job?.error, ...details].filter(Boolean).join('\n')}</pre>
            </details>
          )}
        </div>
        <div className="modal-actions">
          {!sailing && (
            <button type="button" className={phase === 'done' ? 'btn btn-primary update-wide' : 'btn'} onClick={onClose}>
              {phase === 'ready' ? 'Not now' : 'Done'}
            </button>
          )}
          {(phase === 'ready' || phase === 'failed') && status.behind > 0 && (
            <button type="button" className="btn btn-primary" disabled={busy || status.dirty} onClick={() => void act('install')}>
              {phase === 'failed' ? 'Try again' : 'Update'}
            </button>
          )}
        </div>
      </div>
    </div>
  )
}

// failReason is why an update failed, as a sentence ending the "Nothing changed" line.
function failReason(error?: string | null): string {
  if (!error) return '. Try again in a bit.'
  if (/too many confirmations/.test(error)) return ': the app only asks to update a few times an hour. Try again later.'
  if (/confirmation was just shown/.test(error)) return ': an update was just asked for. Try again in a minute.'
  if (/already up to date/.test(error)) return ': you are already up to date.'
  return `: ${error.charAt(0).toUpperCase()}${error.slice(1)}.`
}

type Phase = 'ready' | 'confirming' | 'working' | 'done' | 'failed'

const PHASE_TITLE: Record<Phase, string> = {
  ready: 'An update is ready',
  confirming: 'Confirm the update',
  working: 'Updating…',
  done: 'Updated',
  failed: "The update didn't finish",
}

function updatePhase(status: UpdateStatus, appRun: AppInstall | null, busy: boolean): Phase {
  if (appRun) {
    if (appRun.running) return 'working'
    if (appRun.ok) return 'done'
    // Refused before anything ran (the hourly confirmation limit, say) and installed
    // since by another route: up to date. A run that failed part way stays shown.
    if (status.behind === 0 && appRun.lines.length === 0) return 'done'
    return appRun.declined ? 'ready' : 'failed'
  }
  const state = status.job?.state
  if (state === 'confirming') return 'confirming'
  if (busy) return 'working'
  if (state === 'done' && status.behind === 0) return 'done'
  if (state === 'failed') return 'failed'
  return 'ready'
}

// Commit subjects in plain words: user-facing feat/fix lines lose their
// "feat(app):" prefix; tests, refactors and chores are only counted.
function changelog(commits: UpdateStatus['commits']) {
  const notes: string[] = []
  let hidden = 0
  for (const c of commits) {
    const m = /^(feat|fix)(\([^)]*\))?!?:\s*(.+)$/.exec(c.subject)
    if (m) notes.push(m[3].charAt(0).toUpperCase() + m[3].slice(1))
    else hidden++
  }
  return { notes, hidden }
}
