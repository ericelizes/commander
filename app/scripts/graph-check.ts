// cmdr graph check: reads {snapshot, br: {<project>: BrBead[]}} on stdin, prints
// the Graph-vs-beads result per workstream (JSON), exit 1 on any mismatch.
// Run through scripts/graph-check.sh, which bundles this with esbuild.
import { checkSnapshot, type BrBead } from '../src/renderer/src/graphCheck'
import type { Snapshot } from '../src/shared/snapshot'

const chunks: Buffer[] = []
for await (const c of process.stdin) chunks.push(c as Buffer)
const input = JSON.parse(Buffer.concat(chunks).toString('utf8')) as { snapshot: Snapshot; br: Record<string, BrBead[]>; project?: string }
const results = checkSnapshot(input.snapshot, input.br, input.project)
process.stdout.write(JSON.stringify(results))
process.exit(results.some((r) => r.mismatches.length) ? 1 : 0)
