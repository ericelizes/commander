// `npm run dev:renderer`: the always-on live renderer dev server on
// 127.0.0.1:5173/app/ (the commander-ui launchd/systemd unit runs it). The
// installed app and cmdrd's /app/ load it while it answers, so an edit under
// app/src shows up through HMR with no rebuild or reinstall. strictPort: the
// app and cmdrd look for it on exactly this port.
import { defineConfig } from 'vite'
import { rendererConfig } from './renderer.config'

export default defineConfig(({ command }) => {
  const cfg = rendererConfig(command)
  return { ...cfg, server: { ...cfg.server, strictPort: true } }
})
