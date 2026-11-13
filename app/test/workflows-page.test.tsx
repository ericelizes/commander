// @vitest-environment jsdom
import '@testing-library/jest-dom/vitest'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen, within } from '@testing-library/react'
import { WorkflowsView } from '../src/renderer/src/components/WorkflowsView'
import { useStore } from '../src/renderer/src/store'

afterEach(() => { cleanup(); vi.unstubAllGlobals() })

const catalog = { workflows: [
  { id: 'pr-review', title: 'PR review', applies_to: 'a pull request on a shared platform repo', summary: 'Devin, then one review pass.', use_when: ['Shared platform code'], skip_when: ['Docs only'], steps: [{ id: 'devin-comments', title: 'Answer and resolve every Devin comment', actor: 'operator' }, { id: 'review', title: 'Run the code review once', actor: 'operator' }] },
  { id: 'forge-app-release', title: 'Forge app release', applies_to: 'a change to a Forge app', use_when: ['Any change to a Forge app that goes to production'], skip_when: ['Never leaves the dev box'], catalog_only: true, steps: [{ id: 'owner-go', title: 'The app owner gives a written go', actor: 'owner' }, { id: 'check-prod', title: 'Check production once', actor: 'operator', optional: true }] },
] }

describe('Workflows page', () => {
  it('shows each workflow with when to use it, when to skip it and its steps', async () => {
    useStore.setState({ config: { apiUrl: 'http://localhost:8787', token: 't' } })
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(catalog), { status: 200 })))
    render(<WorkflowsView />)
    const review = await screen.findByRole('region', { name: 'PR review' })
    expect(within(review).getByText('Shared platform code')).toBeInTheDocument()
    expect(within(review).getByText('Docs only')).toBeInTheDocument()
    expect(within(review).getAllByRole('listitem').map((li) => li.textContent)).toContain('Run the code review onceAgent')
    const release = screen.getByRole('region', { name: 'Forge app release' })
    expect(within(release).getByText('App owner')).toBeInTheDocument()
    expect(within(release).getByText('Agent · optional')).toBeInTheDocument()
    expect(within(release).getByText(/the board doesn't add them as steps yet/)).toBeInTheDocument()
  })
})
