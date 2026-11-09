import type { CmdResult, CommanderApi, NeedsYouItem, SnapshotLike } from './types'

// Fixture cards for tests and the screenshot gallery: one per answer mode,
// plus a plain flag with no card.
const T0 = 1_792_500_000 // a fixed "now" (unix seconds) so ages are stable

export const FIXTURE_NOW_MS = T0 * 1000

export const optionsCard: NeedsYouItem = {
  project: 'imports',
  bead_id: 'imports-12.3',
  title: 'Pick the import events storage',
  workstream: 'Import pipeline',
  rank: 1,
  since: T0 - 42 * 60,
  decision: {
    question: 'Where should import events be stored?',
    context: 'Imports are written today by the nightly job straight into the records table. The reports team wants them queryable within a minute, and the audit team needs a full history.\n\nBoth options keep the nightly job as a fallback.',
    options: [
      { label: 'Postgres outbox table', implications: 'One migration, no new infra. Queryable immediately; history via the existing audit trigger.', recommended: true },
      { label: 'Kafka topic + consumer', implications: 'New topic and a consumer service to run. Best fan-out later; about 3 more days.' },
      { label: 'Keep the nightly job', implications: 'No work now. Reports keep waiting up to 24h for imports.' },
    ],
  },
}

export const stepsCard: NeedsYouItem = {
  project: 'imports',
  bead_id: 'imports-14.1',
  title: 'Run the staging migration',
  workstream: 'Import pipeline',
  rank: 1,
  since: T0 - 3 * 3600,
  decision: {
    question: 'Run the staging migration (needs your SSO)',
    context: 'The migration is reviewed and merged. Staging needs a human login, so run these from the repo root, then press Done.',
    steps: [
      'aws sso login --profile staging',
      'just db-migrate env=staging',
      'just smoke-test env=staging --suite imports',
    ],
  },
}

export const inputCard: NeedsYouItem = {
  project: 'example-app',
  bead_id: 'example-app-7',
  title: 'Name the launch',
  workstream: 'example-app onboarding',
  rank: 2,
  since: T0 - 2 * 86400,
  decision: {
    question: 'What should we call the public launch?',
    context: 'Marketing needs a name by Thursday for the landing page and the email. Anything short works; the Manager will run it past brand.',
    input: true,
  },
}

export const plainFlag: NeedsYouItem = {
  project: 'commander',
  bead_id: 'cmdr-31',
  title: 'Verify the dmg opens',
  workstream: 'Desktop app',
  rank: 3,
  since: T0 - 5 * 60,
  ask: 'Verify: dmg opens on your Mac',
  note: 'Download the arm64 dmg from the build, drag it to Applications and open it once.',
}

export const fixtureItems: NeedsYouItem[] = [optionsCard, stepsCard, inputCard, plainFlag]

// An in-memory CommanderApi: records every argv and drops resolved flags from
// the snapshot, like cmdrd would.
export interface FakeApi extends CommanderApi {
  calls: string[][]
  copied: string[]
  opened: string[]
  items: NeedsYouItem[]
  next: Partial<CmdResult> | Error | null
}

export function fakeApi(items: NeedsYouItem[] = fixtureItems): FakeApi {
  const api: FakeApi = {
    calls: [],
    copied: [],
    opened: [],
    items: [...items],
    next: null,
    async snapshot(): Promise<SnapshotLike> {
      return { needs_you: api.items }
    },
    async runCmd(argv: string[]): Promise<CmdResult> {
      api.calls.push(argv)
      const next = api.next
      api.next = null
      if (next instanceof Error) throw next
      const res: CmdResult = { ok: true, stdout: '{}', stderr: '', exit: 0, ...next }
      if (res.ok) {
        const bead = argv[argv.indexOf('--bead') + 1]
        api.items = api.items.filter(i => i.bead_id !== bead)
      }
      return res
    },
    async copy(text: string) {
      api.copied.push(text)
    },
    openUrl(url: string) {
      api.opened.push(url)
    },
  }
  return api
}
