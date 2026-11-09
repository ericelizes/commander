#!/usr/bin/env node
// Spike driver: runs Claude Code headlessly in bidirectional stream-json mode
// and records every event. No dependencies.
//
//   node driver.mjs <scenario.json> <out.jsonl>
//
// scenario.json: { cwd, args: [...extra claude args], steps: [...] }
// steps:
//   { "send": "text" }            write a user message to stdin
//   { "wait": "result" }          wait for the next `result` event
//   { "wait": "delta", "n": 5 }   wait until n more text deltas have streamed
//   { "interrupt": true }         send control_request {subtype: interrupt}
//   { "sigint": true }            send SIGINT to the process instead
//   { "sleep": ms }
//   { "close": true }             end stdin (process exits after the turn)
// Permission prompts (control_request can_use_tool) are answered by
// scenario.permit: { "<ToolName>": "allow" | "deny" }, default deny.
import { spawn } from 'node:child_process'
import { readFileSync, createWriteStream } from 'node:fs'
import { createInterface } from 'node:readline'

const [scenarioPath, outPath] = process.argv.slice(2)
const sc = JSON.parse(readFileSync(scenarioPath, 'utf8'))
const out = createWriteStream(outPath)
const t0 = Date.now()
const log = (dir, obj) => out.write(JSON.stringify({ t: Date.now() - t0, dir, ...obj }) + '\n')

const args = ['-p', '--input-format', 'stream-json', '--output-format', 'stream-json', '--verbose', ...sc.args]
log('meta', { argv: ['claude', ...args] })
const child = spawn('claude', args, { cwd: sc.cwd, stdio: ['pipe', 'pipe', 'pipe'], env: { ...process.env, DISABLE_AUTOUPDATER: '1' } })
child.stderr.on('data', (d) => log('stderr', { text: d.toString() }))

let waiters = []
let deltas = 0
let reqN = 0
const send = (obj) => { log('in', { msg: obj }); child.stdin.write(JSON.stringify(obj) + '\n') }

createInterface({ input: child.stdout }).on('line', (line) => {
  let ev
  try { ev = JSON.parse(line) } catch { log('raw', { line }); return }
  log('out', { ev })
  if (ev.type === 'stream_event' && ev.event?.delta?.type === 'text_delta') {
    deltas++
    process.stdout.write(ev.event.delta.text)
  }
  if (ev.type === 'control_request' && ev.request?.subtype === 'can_use_tool') {
    const verdict = (sc.permit || {})[ev.request.tool_name] || 'deny'
    const response = verdict === 'allow'
      ? { behavior: 'allow', updatedInput: ev.request.input }
      : { behavior: 'deny', message: 'denied by spike driver' }
    send({ type: 'control_response', response: { subtype: 'success', request_id: ev.request_id, response } })
  }
  if (ev.type === 'result') process.stdout.write(`\n[result ${ev.subtype} cost=$${ev.total_cost_usd}]\n`)
  waiters = waiters.filter((w) => !w(ev))
})

const until = (pred) => new Promise((res) => waiters.push((ev) => (pred(ev) ? (res(ev), true) : false)))
const exited = new Promise((res) => child.on('exit', (code, sig) => { log('exit', { code, sig }); res() }))

for (const step of sc.steps) {
  if (step.send !== undefined) {
    send({ type: 'user', message: { role: 'user', content: step.send }, parent_tool_use_id: null, session_id: '' })
  } else if (step.wait === 'result') {
    await Promise.race([until((ev) => ev.type === 'result'), exited])
  } else if (step.wait === 'delta') {
    const target = deltas + (step.n || 1)
    await Promise.race([until(() => deltas >= target), exited])
  } else if (step.interrupt) {
    send({ type: 'control_request', request_id: `req_${++reqN}`, request: { subtype: 'interrupt' } })
  } else if (step.sigint) {
    log('meta', { note: 'SIGINT' }); child.kill('SIGINT')
  } else if (step.sleep) {
    await new Promise((r) => setTimeout(r, step.sleep))
  } else if (step.close) {
    child.stdin.end()
  }
}
child.stdin.end()
await Promise.race([exited, new Promise((r) => setTimeout(r, 60000).unref())])
out.end()
