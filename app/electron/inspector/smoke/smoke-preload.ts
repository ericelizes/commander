import { contextBridge } from 'electron'

import { createInspectorWebviewBridge } from '../preload'

contextBridge.exposeInMainWorld('commanderHost', { webview: createInspectorWebviewBridge() })
