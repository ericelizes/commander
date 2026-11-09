// IPC channels between the app shell (main) and the renderer. One list so
// main, preload and renderer agree. See app/build/INTEGRATION.md.

// main -> renderer
export const NAV_EXPAND_OR_GO = 'nav:expand-or-go' // (n: number) 0 = Commander, 1-9 = workstream n
export const NAV_QUICK_SWITCHER = 'nav:quick-switcher'
export const NAV_NEXT_WORKSTREAM = 'nav:next-workstream'
export const NAV_PREV_WORKSTREAM = 'nav:prev-workstream'
export const NAV_SETTINGS = 'nav:settings'
export const NAV_OPEN_WORKSTREAM = 'nav:open-workstream' // (id: string) from the dock menu
export const DAEMON_STATE = 'daemon:state' // (state: DaemonState)
export const UPDATE_REMOTE_LINE = 'update:remote-line' // (line: string) output of the install over SSH

// renderer -> main
export const DAEMON_GET_STATE = 'daemon:get-state' // invoke -> DaemonState
export const DOCK_SET_NEEDS_YOU = 'dock:set-needs-you' // (count: number)
export const APP_CAPTURE = 'app:capture' // invoke -> base64 PNG of this window (webContents.capturePage)
export const UPDATE_REMOTE_RUN = 'update:remote-run' // invoke ('install' | 'rollback') -> RemoteUpdateResult; main shows the dialog
export const DOCK_SET_WORKSTREAMS = 'dock:set-workstreams' // (list: DockWorkstream[])

export interface DockWorkstream {
  id: string
  name: string
}
