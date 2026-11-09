// Does attaching/resizing the app's grouped session fire cmdr's
// client-attached / client-resized hooks (which run `cmdr __relayout`, whose
// resize-pane calls unzoom the window)? Simulates cmdr's hooks on `commander:`
// with a resize-pane like Relayout's.
'use strict'
const path = require('path')
const { execFileSync } = require('child_process')
const { Browser, sleep, tmux, realClient } = require('./lib')

;(async () => {
  execFileSync('sh', [path.join(__dirname, '../fixture/up.sh')], { env: { ...process.env, TMUX: '' } })
  for (const h of ['client-attached', 'client-resized']) {
    tmux('set-hook', '-t', 'commander:', h, `set -g @fired_${h.replace('-', '_')} 1 ; resize-pane -t %1 -x 30`)
  }
  const zoomed = () => tmux('display', '-p', '-t', 'commander:1', '#{window_zoomed_flag}')
  const fired = () => tmux('display', '-p', 'attached=#{@fired_client_attached} resized=#{@fired_client_resized}')
  const a = new Browser({ client: 'h', cols: 100, rows: 30 })
  await a.open(); await a.waitFor(/TRUECOLOR/); await sleep(400)
  console.log(`app attached (grouped session): hooks on commander: fired? ${fired()} zoomed=${zoomed()}`)
  a.resize(110, 35); await sleep(400)
  console.log(`app resized: ${fired()} zoomed=${zoomed()}`)
  const real = realClient(200, 50); await sleep(600)
  console.log(`real TUI client attaches to commander: ${fired()} zoomed=${zoomed()}  <- Relayout-style resize-pane unzoomed the app's view`)
  real.kill(); a.close(); await sleep(200)
  process.exit(0)
})().catch((e) => { console.error(e.stack || e); process.exit(1) })
