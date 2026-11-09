// Main-process entry of the inspector feature. Preload code imports
// ./preload instead (it needs ipcRenderer, not ipcMain).
export { registerInspectorIpc, setupPartition, type InspectorDeps, type InspectorHandle } from './ipc'
export { PARTITION, SITES } from './policy'
export { CH, type NavMsg, type ShortcutMsg, type SyncMsg, type ViewState } from './contract'
