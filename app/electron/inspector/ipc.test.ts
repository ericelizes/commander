import { describe, expect, it, vi } from 'vitest'

// No Electron here: stand-ins, so registerInspectorIpc's own wiring of the
// question is what runs (a dropped parent or button check fails here).
const m = vi.hoisted(() => ({
  show: vi.fn((_parent: unknown, _options: unknown) => 1),
  fromWebContents: vi.fn((_wc: unknown): unknown => null),
}))

vi.mock('electron', () => ({
  app: { getPath: () => '/nonexistent/inspector-ipc-test' },
  BrowserWindow: { fromWebContents: m.fromWebContents },
  dialog: { showMessageBoxSync: m.show },
  ipcMain: { on: () => {}, off: () => {}, handle: () => {}, removeHandler: () => {} },
  session: { fromPartition: () => ({ setUserAgent: () => {}, setPermissionRequestHandler: () => {}, setPermissionCheckHandler: () => {} }) },
  shell: {},
  clipboard: {},
  Menu: {},
  WebContentsView: class {},
}))
vi.mock('../context-menu', () => ({ attachContextMenu: () => {} }))

const { registerInspectorIpc } = await import('./ipc')

describe('registerInspectorIpc', () => {
  const win = { isDestroyed: () => false, on: () => {}, webContents: {} }
  const gate = () => (registerInspectorIpc(win as never).views as unknown as { opts: { providerGate: { ask: (p: string, h: string, a: never) => string } } }).opts.providerGate

  it('asks the real dialog, in the window that owns the page', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {}) // the test path cannot be saved
    const page = {} as never
    const signInWindow = { isDestroyed: () => false }
    m.fromWebContents.mockReturnValueOnce(signInWindow)
    expect(gate().ask('provider.example', 'https://box-1.term.example/', page)).toBe('yes')
    expect(m.fromWebContents).toHaveBeenCalledWith(page)
    expect(m.show.mock.calls[0][0]).toBe(signInWindow)
    expect(m.show.mock.calls[0][1]).toMatchObject({ message: 'Continue to provider.example in this tab?' })
    warn.mockRestore()
  })

  it('asks in the host window for a tab, and any other button is a no', () => {
    m.show.mockClear()
    m.show.mockReturnValueOnce(0)
    expect(gate().ask('provider.example', 'https://box-1.term.example/', {} as never)).toBe('no')
    expect(m.show.mock.calls[0][0]).toBe(win)
  })
})
