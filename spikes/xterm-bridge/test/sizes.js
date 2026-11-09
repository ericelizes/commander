// Size negotiation: the app client (grouped session, zoomed chat) vs a "real"
// TUI client attached to `commander`, and vs a second app client.
// Prints the measured window/pane/client sizes after every step.
'use strict'
const path = require('path')
const { execFileSync } = require('child_process')
const { Browser, sleep, tmux, realClient, panes, windowInfo, clients } = require('./lib')

const up = () => execFileSync('sh', [path.join(__dirname, '../fixture/up.sh')], { env: { ...process.env, TMUX: '' } })
const state = (label) => console.log(`  ${label.padEnd(44)} ${windowInfo()} | ${panes()} | clients: ${clients()}`)

async function scenario(name, wsize, realOnWindow) {
  console.log(`\n## ${name}`)
  up()
  if (wsize) tmux('set-option', '-w', '-t', 'commander:1', 'window-size', wsize)
  tmux('select-window', '-t', `commander:${realOnWindow}`)
  const real = realClient(200, 50)
  await sleep(300)
  state(`real TUI client 200x50 on window ${realOnWindow}`)
  const a = new Browser({ client: 'a', cols: 100, rows: 30 })
  await a.open(); await a.waitFor(/TRUECOLOR/); await sleep(300)
  state('app A 100x30 attached')
  a.resize(120, 40); await sleep(300)
  state('app A resized to 120x40')
  real.write('x'); await sleep(300) // the TUI client becomes "latest"
  state('real client typed a key')
  const b = new Browser({ client: 'b', cols: 80, rows: 24 })
  await b.open(); await b.waitFor(/TRUECOLOR/); await sleep(300)
  state('app B 80x24 attached (2nd tab, same chat)')
  a.send('q'); await sleep(300)
  state('app A typed a key')
  // How app A's 120x40 screen looks when the window is not its size.
  b.send('q'); await sleep(300)
  state('app B typed a key')
  console.log('  app A screen (120x40 terminal) last non-empty lines:')
  console.log(a.screen().split('\n').filter((l) => l.trim()).slice(-3).map((l) => '    |' + l).join('\n'))
  a.close(); b.close(); real.kill(); await sleep(300)
}

;(async () => {
  await scenario('window-size latest (tmux default), TUI on the SAME window', null, 1)
  await scenario('window-size latest, TUI on ANOTHER window (0)', null, 0)
  await scenario('window-size smallest, TUI on the same window', 'smallest', 1)
  await scenario('window-size largest, TUI on the same window', 'largest', 1)
  process.exit(0)
})().catch((e) => { console.error(e.message); process.exit(1) })
