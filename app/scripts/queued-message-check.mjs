#!/usr/bin/env node
// Real browser geometry and jump interaction, using only the demo chat engine.
// Build dist first (through a build slot). Keeps dark/light shots in out/queue/.
// Each theme x size runs with the demo's short reply and with a reply taller
// than the view (reply=long), where reaching the latest must not move it.
// Extra arguments go to every smoke run (e.g. --dist <dir>).
import { readFileSync } from 'node:fs'
import { spawn } from 'node:child_process'
import { resolve } from 'node:path'
const app = resolve(import.meta.dirname, '..')
const scenario = JSON.parse(readFileSync(resolve(app, '../tests/evals/scenarios/queued-message-layout.json'), 'utf8'))
const probe = (reply) => `(async () => {
  const realTimeout = window.setTimeout.bind(window);
  const sleep = ms => new Promise(r => realTimeout(r, ms));
  // Hold the demo reply mid-turn so slow shared hosts cannot drain the queue
  // during the geometry assertions. Short React scheduling timers still run.
  const replyTimers = [];
  let hold = false;
  window.setTimeout = (fn, ms, ...args) => {
    if (ms >= 700 && hold) return 0;
    const id = realTimeout(fn, ms, ...args);
    if (ms >= 700) replyTimers.push(id);
    return id;
  };
  const chat = document.querySelector('.cmdr-chat');
  if (!chat) throw Error('No fixture chat');
  const box = chat.querySelector('textarea');
  const send = async text => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set.call(box, text);
    box.dispatchEvent(new Event('input', {bubbles:true}));
    await sleep(30);
    chat.querySelector('[aria-label="Send"]').click();
    await sleep(50);
  };
  const problems = [];
  const check = (ok, why) => { if (!ok) problems.push(why); };
  const queue = () => chat.querySelector('.cc-waiting');
  const newestShown = () => {
    const q = queue(), last = q?.lastElementChild;
    if (!last) return false;
    const a = q.getBoundingClientRect(), b = last.getBoundingClientRect();
    return b.top >= a.top - 1 && b.bottom <= a.bottom + 1;
  };
  await send('Start the sample task');
  await sleep(1100);
  hold = true;
  replyTimers.forEach(clearTimeout);
  for (let i = 0; i < ${scenario.queueCount}; i++) {
    await send(${JSON.stringify(scenario.messages)}[i % ${scenario.messages.length}]);
    if (i >= 3) check(newestShown(), 'newest queued send out of view after send ' + (i + 1));
  }
  await sleep(100);
  const viewport = chat.querySelector('.ct-viewport');
  const composer = chat.querySelector('.cc-composer');
  const wrap = chat.querySelector('.ct-jump-wrap');
  const jump = chat.querySelector('[aria-label="Jump to latest"]');
  const q0 = queue();
  check(!!q0 && q0.children.length >= ${scenario.queueCount}, 'queued sends missing');
  if (!q0) return {problems};
  const q = q0.getBoundingClientRect(), c = composer.getBoundingClientRect(), v = viewport.getBoundingClientRect();
  check(composer.contains(q0), 'queue is outside composer');
  check(v.bottom <= c.top + 1 && q.top >= c.top && q.bottom <= c.bottom, 'queue overlaps conversation');
  check(q.height <= 121 && q0.scrollHeight > q0.clientHeight, 'long queue is not bounded and scrollable');
  check(q0.scrollWidth <= q0.clientWidth + 1, 'queue scrolls sideways');
  check(newestShown(), 'newest queued send out of view');
  check(viewport.clientHeight >= ${scenario.minViewport}, 'conversation squeezed to ' + viewport.clientHeight + 'px');
  check(getComputedStyle(chat.querySelector('.cc-composer-wrap')).backgroundColor === 'rgba(0, 0, 0, 0)', 'opaque composer band');

  // A reply is revealed word by word: measure scrolling once its height holds.
  for (let last = -1, same = 0, t = 0; same < 3 && t < 75; t++) {
    await sleep(200);
    same = viewport.scrollHeight === last ? same + 1 : 0;
    last = viewport.scrollHeight;
  }
  const height = viewport.clientHeight;
  const max = () => viewport.scrollHeight - viewport.clientHeight;
  const settle = async () => { viewport.dispatchEvent(new Event('scroll')); await sleep(250); };
  // The jump row keeps one height in every state; only the button hides.
  const geometry = (state) => {
    const w = wrap.getBoundingClientRect(), j = jump.getBoundingClientRect(), vb = viewport.getBoundingClientRect();
    check(viewport.clientHeight === height, state + ': viewport resized ' + height + ' -> ' + viewport.clientHeight);
    check(w.height > 0 && w.top >= vb.bottom - 1 && w.bottom <= composer.getBoundingClientRect().top + 1, state + ': jump row overlaps chat or composer');
    check(j.top >= w.top - 1 && j.bottom <= w.bottom + 1, state + ': jump outside its row');
    return w.height;
  };
  const latest = (state) => {
    check(jump.disabled && getComputedStyle(jump).visibility === 'hidden', state + ': jump shown at latest');
    check(viewport.scrollTop >= max() - 1, state + ': not at latest');
  };
  ${reply === 'long' ? "check(chat.querySelector('.ct-pair.last').getBoundingClientRect().height > height + 100, 'latest reply is not taller than the view');" : ''}

  // At latest, reached by scrolling.
  viewport.scrollTop = max();
  await settle();
  const atLatestTop = viewport.scrollTop;
  const rowAtLatest = geometry('at latest');
  latest('at latest');
  await sleep(250);
  check(viewport.scrollTop === atLatestTop, 'at latest: scroll moved ' + atLatestTop + ' -> ' + viewport.scrollTop);

  // Scrolled up.
  viewport.scrollTop = 0;
  await settle();
  const j = jump.getBoundingClientRect();
  check(!jump.disabled && getComputedStyle(jump).visibility === 'visible' && j.height > 0, 'jump unavailable above latest');
  check(geometry('scrolled') === rowAtLatest, 'jump row changed height');

  // Jumped back.
  const target = max();
  jump.click();
  await sleep(250);
  check(target > 0 && viewport.scrollTop >= target - 1, 'jump did not reach latest');
  const jumpedTop = viewport.scrollTop;
  await settle();
  geometry('jumped');
  latest('jumped');
  check(viewport.scrollTop === jumpedTop, 'jumped: scroll moved ' + jumpedTop + ' -> ' + viewport.scrollTop);
  await sleep(1000);
  latest('a second later');
  check(viewport.scrollTop === jumpedTop, 'a second later: scroll moved ' + jumpedTop + ' -> ' + viewport.scrollTop);
  return {problems, reply:${JSON.stringify(reply)}, viewport:height, jumpRow:rowAtLatest, queueHeight:q.height, scrollTop:viewport.scrollTop, scrollMax:max()};
})().then(result => window.__queueLayout = result).catch(e => window.__queueLayout = {problems:[String(e)]})`
// Stopped (a test deadline, Ctrl-C): pass the signal on to the smoke run and
// wait for it, so it ends its own browser before this process exits.
let child
let stopped = false
for (const sig of ['SIGTERM', 'SIGINT', 'SIGHUP']) {
  process.on(sig, () => {
    stopped = true
    child?.kill(sig)
  })
}
const smoke = (args) =>
  new Promise((done) => {
    let out = ''
    child = spawn(process.execPath, ['scripts/smoke.mjs', ...args], { cwd: app })
    child.stdout.on('data', (d) => (out += d))
    child.stderr.on('data', (d) => (out += d))
    const timer = setTimeout(() => child?.kill('SIGTERM'), 120_000)
    child.on('error', (e) => (out += String(e)))
    child.on('close', (status) => {
      clearTimeout(timer)
      child = undefined
      done({ status, out })
    })
  })
const extra = process.argv.slice(2)
let failed = false
for (const reply of scenario.replies) for (const theme of scenario.themes) for (const size of scenario.sizes) {
  if (stopped) break
  const query = reply === 'long' ? `${scenario.query}&reply=long` : scenario.query
  const result = await smoke([...extra, '--offline', '--query', query, '--theme', theme, '--width', String(size.width), '--height', String(size.height), '--scale', '1', '--out', `out/queue/${reply}-${theme}-${size.width}.png`, '--eval', probe(reply), '--expect', 'JSON.stringify(window.__queueLayout)', '--expect', 'window.__queueLayout?.problems.length === 0'])
  console.log(`${reply} ${theme} ${size.width}x${size.height}\n${result.out}`)
  if (result.status !== 0) failed = true
}
process.exitCode = failed || stopped ? 1 : 0
