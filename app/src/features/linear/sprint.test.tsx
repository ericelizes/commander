import { StrictMode } from 'react'
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { useStore } from '../../renderer/src/store'
import { LinearSettings } from './LinearSettings'
import { LinearPill } from './Ticket'
import { touched, mismatch, useLinearSprint, useSprint, type LinearIssue } from './sprint'
const issue: LinearIssue = { id: 'TEAM-1', title: 'Example', status_name: 'Done', status_type: 'completed', url: '' }
beforeEach(() => {
 useStore.setState({ config: { apiUrl: 'http://fake', token: 'test' } })
 useSprint.setState({ data: { state: 'ok', key: 'saved', issues: [issue] } })
})
afterEach(() => { cleanup(); vi.unstubAllGlobals(); vi.useRealTimers(); useSprint.setState({ data: null }) })
it('shows ticket status and both mismatch directions', () => {
 render(<LinearPill id="TEAM-1" progress={{done: 1,total: 3}} />)
 expect(screen.getByText(/TEAM-1/)).toHaveTextContent('Done')
 expect(screen.getByLabelText('Ticket Done, 2 open steps')).toBeTruthy()
 expect(mismatch({...issue,status_name:'In Review',status_type:'started'},{done:3,total:3})).toBe('Steps done, ticket still In Review')
 expect(mismatch(issue,{done:3,total:3})).toBeUndefined()
 expect(mismatch(issue,{done:0,total:0})).toBeUndefined()
})
it('clears the password before send resolves and removes the saved key', async () => {
 let finish!: (v: unknown) => void
 const fetcher = vi.fn().mockImplementationOnce(() => new Promise((r) => { finish=r })).mockResolvedValue({ok:true})
 vi.stubGlobal('fetch', fetcher)
 render(<LinearSettings />)
 const input=screen.getByLabelText('Linear API key')
 expect(input).toHaveAttribute('type','password'); expect(input).toHaveAttribute('autocomplete','off')
 fireEvent.change(input,{target:{value:'private-test-value'}})
 fireEvent.click(screen.getByRole('button',{name:'Replace'}))
 expect(input).toHaveValue('')
 expect(fetcher.mock.calls[0][1].body).toBe('{"key":"private-test-value"}')
 await act(async () => finish({ok:true}))
 fireEvent.click(screen.getByRole('button',{name:'Remove'}))
 await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('not set'))
 expect(fetcher.mock.calls[1][1].method).toBe('DELETE')
 expect(document.body.textContent).not.toContain('private-test-value')
})
it('uses generic errors after failure', async () => {
 vi.stubGlobal('fetch',vi.fn().mockRejectedValue(new Error('private-test-value')))
 render(<LinearSettings />)
 fireEvent.change(screen.getByLabelText('Linear API key'),{target:{value:'private-test-value'}})
 fireEvent.click(screen.getByRole('button',{name:'Replace'}))
 await screen.findByRole('alert')
 expect(screen.getByLabelText('Linear API key')).toHaveValue('')
 expect(document.body.textContent).not.toContain('private-test-value')
})
it('polls every 30 seconds and stops on unmount', async () => {
 vi.useFakeTimers()
 const fetcher=vi.fn().mockResolvedValue({ok:true,json:async()=>({state:'ok',key:'saved',issues:[]})})
 vi.stubGlobal('fetch',fetcher)
 function Reader(){useLinearSprint();return null}
 const view=render(<StrictMode><Reader /></StrictMode>)
 await act(async()=>{})
 expect(fetcher).toHaveBeenCalledTimes(1)
 await act(async()=>{vi.advanceTimersByTime(29_999)})
 expect(fetcher).toHaveBeenCalledTimes(1)
 await act(async()=>{vi.advanceTimersByTime(1)})
 expect(fetcher).toHaveBeenCalledTimes(2)
 view.unmount()
 await act(async()=>{vi.advanceTimersByTime(30_000)})
 expect(fetcher).toHaveBeenCalledTimes(2)
})

it('keeps canceled tickets neutral even when steps are done', () => {
 const canceled = {...issue, status_name:'Cancelled', status_type:'canceled'}
 useSprint.setState({data:{state:'ok',key:'saved',issues:[canceled]}})
 render(<LinearPill id="TEAM-1" progress={{done:3,total:3}} />)
 expect(screen.getByText(/TEAM-1/)).toHaveTextContent('Cancelled')
 expect(screen.queryByLabelText(/Steps done/)).toBeNull()
 expect(touched([canceled])).toBe(0)
 expect(mismatch(canceled,{done:0,total:3})).toBeUndefined()
})
it('refreshes ticket pills immediately after saving within the read gate', async () => {
 const fetcher=vi.fn()
  .mockResolvedValueOnce({ok:true,json:async()=>({state:'no_key',key:'not set',issues:[]})})
  .mockResolvedValueOnce({ok:true})
  .mockResolvedValueOnce({ok:true,json:async()=>({state:'ok',key:'saved',issues:[issue]})})
 vi.stubGlobal('fetch',fetcher)
 function Reader(){useLinearSprint();return <LinearPill id="TEAM-1" />}
 render(<><Reader /><LinearSettings /></>)
 await waitFor(()=>expect(screen.getByRole('status')).toHaveTextContent('not set'))
 fireEvent.change(screen.getByLabelText('Linear API key'),{target:{value:'private-test-value'}})
 fireEvent.click(screen.getByRole('button',{name:'Save'}))
 await waitFor(()=>expect(screen.getByText(/TEAM-1/)).toHaveTextContent('Done'))
 expect(fetcher).toHaveBeenCalledTimes(3)
 expect(fetcher.mock.calls[2][0]).toBe('http://fake/api/linear')
})
