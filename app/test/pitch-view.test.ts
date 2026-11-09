import { describe, expect, it } from 'vitest'
import { flowSteps, viewSteps } from '../src/renderer/src/components/ExecutionFlow'
import { glanceStats, pitchMarkdown, pitchSections, spaceNumbers } from '../src/renderer/src/components/PitchView'

describe('pitchMarkdown', () => {
  const graph = { nodes: [{ id: 'g1', title: 'VM rebuilt with the fix', gate: true }, { id: 'w', title: 'Re-run setup' }] }
  const md = `Approve VM rebuilt with the fix?
**What's wrong**

| VM | What |
|---|---|
| a | failed |

**The plan**

\`\`\`cmdr-graph
${JSON.stringify(graph)}
\`\`\`

| Gate | Approving it means | Who |
|---|---|---|
| VM rebuilt with the fix | Re-run setup on that VM only | you |
`

  it('turns bold lines into headings, names a graph-only plan the execution workflow, and folds the gate table into the gates', () => {
    const out = pitchMarkdown(md, 'Approve VM rebuilt with the fix?')
    expect(out).not.toContain('Approve VM rebuilt with the fix?')
    expect(out).toContain("### What's wrong")
    expect(out).toContain('### Execution workflow')
    expect(out).not.toContain('Approving it means')
    const spec = JSON.parse(/```cmdr-graph\n([\s\S]*?)```/.exec(out)![1])
    expect(spec.nodes[0].means).toBe('Re-run setup on that VM only')
    expect(out).toContain('| a | failed |')
  })

  it('keeps a written plan as the plan', () => {
    const out = pitchMarkdown("**The plan**\n\n- Rebuild one VM.\n\n**Agentic workflow**\n\n```cmdr-graph\n{}\n```")
    expect(out).toContain('### The plan\n\n- Rebuild one VM.')
    expect(out.match(/### Execution workflow/g)).toHaveLength(1)
  })
})

describe('punctuation', () => {
  it('puts the space back after a squeezed comma or semicolon, but not in code or links', () => {
    const out = pitchMarkdown('| Healthy;routine info | a,b |\n\nSee `x,y` and https://a.b/c,d then behavior,rechecking.\n\n```cmdr-graph\n{"a":1,"b":2}\n```')
    expect(out).toContain('Healthy; routine info')
    expect(out).toContain('`x,y`')
    expect(out).toContain('https://a.b/c,d')
    expect(out).toContain('behavior, rechecking')
    expect(out).toContain('{"a":1,"b":2}')
  })
})

describe('flowSteps', () => {
  it('puts checkpoints on their approver\'s line and work on its doer\'s, keeping only the actors in play', () => {
    const { actors, steps } = flowSteps({
      nodes: [
        { title: 'Fix merged', gate: true, state: 'done', approvers: ['user'], criteria: 'Merged to main' },
        { title: 'Deploy the fix', by: 'automatic' },
        { title: 'Fix deployed to production', gate: true, approvers: ['manager'] },
        { title: 'Re-run setup on the VM' },
        { title: 'PR reviewed', gate: true, approvers: ['reviewer-a'] },
      ],
    })
    expect(actors.map((a) => a.label)).toEqual(['You', 'Manager', 'Operators', 'Reviewers', 'Automatic'])
    expect(steps.map((s) => [s.title, s.actor, s.checkpoint, s.done])).toEqual([
      ['Fix merged', 'you', true, true],
      ['Deploy the fix', 'automatic', false, false],
      ['Fix deployed to production', 'manager', true, false],
      ['Re-run setup on the VM', 'operators', false, false],
      ['PR reviewed', 'reviewers', true, false],
    ])
    expect(steps[0].criteria).toBe('Merged to main')
    expect(steps[4].who).toBe('reviewer-a')
  })
})

describe('pitchSections', () => {
  it('splits at headings and knows each section by its title', () => {
    const secs = pitchSections("Intro line\n\n### What's wrong\n\n| a |\n\n### The plan\n\n- one\n\n### Execution workflow\n\nx")
    expect(secs.map((s) => [s.title, s.kind])).toEqual([
      ['', 'other'],
      ["What's wrong", 'wrong'],
      ['The plan', 'plan'],
      ['Execution workflow', 'workflow'],
    ])
    expect(secs[2].body).toBe('- one')
  })
})

describe('glanceStats', () => {
  it('reads "At a glance" bullets as big numbers', () => {
    expect(glanceStats('- **151** commits master is behind\n- **89%**: disk used\nnot a stat')).toEqual([
      { value: '151', label: 'commits master is behind' },
      { value: '89%', label: 'disk used' },
    ])
    expect(pitchSections('### At a glance\n\n- **2** VMs')[0].kind).toBe('glance')
  })
})

describe('inline section names', () => {
  it('finds a section name written inline, bold or with a colon, but not inside a sentence', () => {
    const out = pitchMarkdown("What's wrong The workbench CLI has no browser login.\n\nThe plan\n\n- Research.\n\n**Execution workflow:** 1. Research done.\n\nThe plan is to wait.\nPlan to run it.")
    expect(out).toContain("### What's wrong\n\nThe workbench CLI has no browser login.")
    expect(out).toContain('### The plan\n\n- Research.')
    expect(out).toContain('### Execution workflow\n\n1. Research done.')
    expect(out).toContain('The plan is to wait.')
    expect(out).toContain('Plan to run it.')
    expect(pitchSections(out).map((x) => x.kind)).toEqual(['wrong', 'plan', 'workflow'])
  })
})

describe('in short', () => {
  it('reads "In short" as the summary', () => {
    const secs = pitchSections(pitchMarkdown("**In short**\n\nWe check why audits time out. Yes runs one request.\n\n**The plan**\n\n- one"))
    expect(secs.map((x) => x.kind)).toEqual(['summary', 'plan'])
  })
})

describe('flowSteps changes', () => {
  it('carries a proposed change and where the work is', () => {
    const { steps } = flowSteps({
      nodes: [
        { title: 'Audits ran on 50 apps', gate: true, state: 'done', approvers: ['user'] },
        { title: 'Run the remaining 31 audits', state: 'in-progress', by: 'operator' },
        { title: 'Time each network step once', by: 'operator', change: 'add' },
        { title: 'Results reviewed', gate: true, approvers: ['user'] },
      ],
    })
    expect(steps.map((s) => [s.title, s.now, s.change])).toEqual([
      ['Audits ran on 50 apps', false, undefined],
      ['Run the remaining 31 audits', true, undefined],
      ['Time each network step once', false, 'add'],
      ['Results reviewed', false, undefined],
    ])
  })
})

describe('viewSteps', () => {
  it('shows the workflow before (as approved) and after (as proposed)', () => {
    const { steps } = flowSteps({
      nodes: [
        { title: 'Kept', gate: true, approvers: ['user'] },
        { title: 'Added', change: 'add' },
        { title: 'Dropped', change: 'remove' },
        { title: 'Changed', change: 'edit' },
      ],
    })
    expect(viewSteps(steps, 'before').map((s) => [s.title, s.change])).toEqual([['Kept', undefined], ['Dropped', undefined], ['Changed', undefined]])
    expect(viewSteps(steps, 'after').map((s) => s.title)).toEqual(['Kept', 'Added', 'Changed'])
    expect(viewSteps(steps, 'changes')).toHaveLength(4)
  })
})


describe('spaceNumbers', () => {
  it('puts back the spaces a model dropped around numbers', () => {
    expect(spaceNumbers('in about86seconds:2 clean and18 dirty, on20 more, at23 apps, versus1.68%')).toBe('in about 86 seconds: 2 clean and 18 dirty, on 20 more, at 23 apps, versus 1.68%')
    expect(pitchMarkdown('Scheduler ok;2 clean,18 dirty')).toBe('Scheduler ok; 2 clean, 18 dirty')
  })
  it('ends a table at a sentence written straight under it', () => {
    expect(pitchMarkdown('| a | b |\n|---|---|\n| 1 | 2 |\nThe archive is confirmed.')).toBe('| a | b |\n|---|---|\n| 1 | 2 |\n\nThe archive is confirmed.')
  })
  it('leaves ids, hashes, times and thousands alone', () => {
    const keep = 'notes/ticket42_draft_plan.md run_8cases exp3 llm5 v2 sample-svc-k3p2.10 abcde12f34a fbe217cda 10:30 1,000 #5A 2fa 10-second p95'
    expect(spaceNumbers(keep)).toBe(keep)
  })
})
