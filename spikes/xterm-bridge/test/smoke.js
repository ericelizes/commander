// Smoke: connect, see the chat, type, see only the chat get it.
'use strict'
const { Browser, sleep, capture, panes, windowInfo, clients } = require('./lib')

;(async () => {
  const b = new Browser({ client: 'smoke', cols: 100, rows: 30 })
  await b.open()
  await b.waitFor(/TRUECOLOR-orange/)
  b.send('hello\r')
  await b.waitFor(/IN b'hello\\r'/)
  console.log(windowInfo(), '|', panes(), '|', clients())
  console.log('--- browser screen ---\n' + b.screen().replace(/\n+$/, ''))
  for (const p of ['%1', '%3']) console.log(`--- ${p} ---\n` + capture(p).replace(/\n+$/, ''))
  b.close()
  await sleep(200)
  process.exit(0)
})().catch((e) => { console.error(e.message); process.exit(1) })
