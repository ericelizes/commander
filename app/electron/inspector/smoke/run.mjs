// Opt-in: builds the inspector preview page and the smoke main/preload, then
// runs Electron hidden (see smoke-main.ts). Not part of `npm test` because it
// launches Electron. Usage (from app/): node electron/inspector/smoke/run.mjs [out.png]
import { spawn } from 'node:child_process'
import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { build as esbuild } from 'esbuild'
import { build as viteBuild } from 'vite'
import react from '@vitejs/plugin-react'
import electronPath from 'electron'

const here = dirname(fileURLToPath(import.meta.url))
const appDir = resolve(here, '../../..')
const out = resolve(process.argv[2] ?? join(appDir, '../docs/screens/app-inspector-electron.png'))
const tmp = mkdtempSync(join(tmpdir(), 'insp-smoke-'))

await viteBuild({
  root: join(appDir, 'src/features/inspector/preview'),
  base: './',
  plugins: [react()],
  logLevel: 'warn',
  build: { outDir: join(tmp, 'page'), emptyOutDir: true },
})
for (const name of ['smoke-main', 'smoke-preload']) {
  await esbuild({
    entryPoints: [join(here, `${name}.ts`)],
    outfile: join(tmp, `${name}.cjs`),
    bundle: true,
    platform: 'node',
    format: 'cjs',
    external: ['electron'],
    logLevel: 'warning',
  })
}

const child = spawn(electronPath, [join(tmp, 'smoke-main.cjs')], {
  stdio: 'inherit',
  env: { ...process.env, SMOKE_PAGE_DIR: join(tmp, 'page'), SMOKE_OUT: out, SMOKE_PRELOAD: join(tmp, 'smoke-preload.cjs') },
})
const kill = setTimeout(() => child.kill('SIGKILL'), 60_000)
child.on('exit', (code) => {
  clearTimeout(kill)
  process.exit(code ?? 1)
})
