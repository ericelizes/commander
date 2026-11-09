import { describe, expect, it } from 'vitest'
import { completeMarkdown, rehypeFadeWords } from '../thread/streaming'

describe('completeMarkdown', () => {
  it('closes an open code fence and balances inline markers on the last line', () => {
    expect(completeMarkdown('```go\nfunc x')).toBe('```go\nfunc x\n```')
    expect(completeMarkdown('the **Data')).toBe('the **Data**')
    expect(completeMarkdown('run `npm')).toBe('run `npm`')
  })

  it('holds back a half-typed link and a bare block marker', () => {
    expect(completeMarkdown('see [the docs](https://exa')).toBe('see ')
    expect(completeMarkdown('Intro\n## ')).toBe('Intro')
  })
})

describe('rehypeFadeWords', () => {
  it('fades only the words past what was already shown, and never inside code blocks', () => {
    const tree = {
      type: 'root',
      children: [
        { type: 'element', tagName: 'p', children: [{ type: 'text', value: 'one two three' }] },
        { type: 'element', tagName: 'pre', children: [{ type: 'text', value: 'code here' }] },
      ],
    }
    rehypeFadeWords({ settledChars: 4 })(tree)
    const words = (tree.children[0] as unknown as { children: { properties: { className?: string[] } }[] }).children
    expect(words.map((w) => !!w.properties.className)).toEqual([false, true, true])
    expect((tree.children[1] as { children: { type: string }[] }).children[0].type).toBe('text')
  })
})
