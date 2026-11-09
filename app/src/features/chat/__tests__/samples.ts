import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

// Invented protocol examples; no recorded account or session data.
const dir = resolve(__dirname, './testdata')

export function sample(name: 's1' | 's2' | 's3' | 's4'): string {
  return readFileSync(resolve(dir, `${name}.jsonl`), 'utf8')
}
