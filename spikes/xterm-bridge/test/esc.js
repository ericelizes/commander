// A lone Esc (Claude Code: interrupt / dismiss) — when does it reach the pane?
'use strict'
const { Browser, sleep, tmux, capture } = require('./lib')
;(async () => {
  require('child_process').execFileSync('sh', [require('path').join(__dirname, '../fixture/up.sh')], { env: { ...process.env, TMUX: '' } })
  console.log(tmux('show', '-s', 'escape-time'), '|', tmux('show', '-s', 'extended-keys'))
  const chat = tmux('list-panes', '-t', 'commander:1', '-F', '#{pane_id} #{@cmdr_chat}').split('\n').find((l) => l.endsWith(' 1')).split(' ')[0]
  const b = new Browser({ client: 'e', cols: 100, rows: 30 })
  const replies = []
  b.term.onData((d) => replies.push(d))
  await b.open(); await b.waitFor(/TRUECOLOR/)
  await sleep(300)
  const queries = b.raw.match(/\x1b\[[>?=]?[0-9;]*[cnq]|\x1b\][0-9]+;\?[^\x07\x1b]*(\x07|\x1b\\)|\x1bP[^\x1b]*\x1b\\/g) || []
  console.log('tmux -> terminal queries:', JSON.stringify(queries))
  console.log('terminal -> tmux replies:', JSON.stringify(replies))
  for (const wait of [100, 600, 1500]) {
    b.send('\x1b')
    await sleep(wait)
    const last = capture(chat).split('\n').filter((l) => l.startsWith('IN ')).pop()
    console.log(`Esc, then ${wait}ms later the pane's last input: ${last}`)
    b.send('.'); await sleep(300)
  }
  // Exact delay, polled every 5ms, over 5 tries.
  const count = () => capture(chat, ['-S', '-']).split('\n').filter((l) => l === "IN b'\\x1b'").length
  for (let i = 0; i < 5; i++) {
    const n0 = count()
    const t0 = Date.now()
    b.send('\x1b')
    while (count() === n0 && Date.now() - t0 < 3000) await sleep(5)
    console.log(`lone Esc reached the pane after ${Date.now() - t0}ms`)
    await sleep(100)
  }
  b.close(); await sleep(100); process.exit(0)
})()
