#!/usr/bin/env node
// Headless synthetic board only; internal-place is intercepted in the page.
import { readFileSync } from 'node:fs'
import { spawnSync } from 'node:child_process'
import { resolve } from 'node:path'
const app = resolve(import.meta.dirname, '..')
const scenario = JSON.parse(readFileSync(resolve(app, '../tests/evals/scenarios/sidebar-drag-undo.json'), 'utf8'))
const probe = `(async () => {
  const sleep = ms => new Promise(r => setTimeout(r, ms));
  const check = (ok, message) => { if (!ok) throw Error(message); };
  const calls = [];
  const fetchOriginal = window.fetch.bind(window);
  window.fetch = (url, init) => {
    if (String(url).endsWith('/api/cmd')) {
      const {argv} = JSON.parse(init.body);
      check(argv[0] === 'internal-place', 'unexpected command');
      calls.push(argv);
      return Promise.resolve(new Response(JSON.stringify({ok:true, stdout:'{}', stderr:'', exit:0})));
    }
    return fetchOriginal(url, init);
  };
  const all = [...document.querySelectorAll('.ws > [data-nav-key]')];
  check(all.length >= 2, 'need two synthetic workstreams');
  const firstKey = all[0].dataset.navKey, movedKey = all[1].dataset.navKey;
  const oldRank = 2;
  const drag = async () => {
    const first = document.querySelector('[data-nav-key="' + CSS.escape(firstKey) + '"]');
    const moved = document.querySelector('[data-nav-key="' + CSS.escape(movedKey) + '"]');
    const a = first.getBoundingClientRect(), b = moved.getBoundingClientRect();
    const x = b.left + 50, y = b.top + b.height / 2;
    moved.dispatchEvent(new PointerEvent('pointerdown', {bubbles:true, button:0, clientX:x, clientY:y}));
    window.dispatchEvent(new PointerEvent('pointermove', {clientX:x + 45, clientY:a.bottom - 1}));
    window.dispatchEvent(new PointerEvent('pointerup'));
    await sleep(100);
  };
  const button = name => [...document.querySelectorAll('button')].find(b => b.textContent.trim() === name);
  await drag();
  check(!!document.querySelector('[role="dialog"]'), 'nest confirmation missing');
  check(calls.length === 0, 'moved before confirmation');
  button('Cancel').click(); await sleep(60);
  check(calls.length === 0, 'cancel moved a row');
  await drag(); button('Nest').click(); await sleep(150);
  check(calls.length === 1 && !!button('Undo'), 'successful drop lacks Undo');
  button('Undo').click(); await sleep(150);
  check(calls.length === 2, 'Undo command missing');
  const undo = calls[1];
  check(undo[undo.indexOf('--parent') + 1] === '' && undo[undo.indexOf('--to') + 1] === String(oldRank), 'wrong inverse placement');
  check(!button('Undo'), 'Undo toast did not clear');
  await drag(); button('Nest').click(); await sleep(150);
  check(!!button('Undo'), 'second drop lacks toast');
  window.__sidebarUndoText = document.querySelector('.nav-move-undo').innerText;
  return window.__sidebarUndoText;
})().catch(e => { console.error('sidebar drag check: ' + e.message); return false; })`
const shots = process.argv.includes('--shots')
const args = shots
  ? ['run', 'shots', '--', '--no-build', '--fake', '--fixture', scenario.fixture, '--themes', scenario.theme, '--out-dir', 'out/sidebar-undo', '--eval', probe]
  : ['scripts/smoke.mjs', '--fake', '--fixture', scenario.fixture, '--theme', scenario.theme, '--out', 'out/sidebar-undo/checked.png', '--eval', probe, '--expect', 'window.__sidebarUndoText && window.__sidebarUndoText.includes("Undo") && window.__sidebarUndoText']
const r = spawnSync(shots ? 'npm' : process.execPath, args, {cwd:app, stdio:'inherit'})
process.exitCode = r.status ?? 1
