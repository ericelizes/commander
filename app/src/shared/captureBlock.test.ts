import { describe, expect, it } from 'vitest'
import { invalidPatterns, isBlocked, normalizePattern, originPath, parsePatterns } from './captureBlock'

describe('captureBlock', () => {
  it('parses lines, comments and case', () => {
    expect(parsePatterns('Foo.com # secret\n\n  *.Bar.org\n')).toEqual(['foo.com', '*.bar.org'])
  })
  it('matches a host, its subdomains and the wildcard form', () => {
    expect(isBlocked('https://foo.com/a', ['foo.com'])).toBe(true)
    expect(isBlocked('https://app.foo.com/a', ['foo.com'])).toBe(true)
    expect(isBlocked('https://foo.com/', ['*.foo.com'])).toBe(true)
    expect(isBlocked('https://notfoo.com/', ['foo.com'])).toBe(false)
    expect(isBlocked('https://foo.com.evil.io/', ['foo.com'])).toBe(false)
    expect(isBlocked('https://evil.com/foo.com', ['foo.com'])).toBe(false)
    expect(isBlocked('https://foo.com@evil.com/', ['foo.com'])).toBe(false)
  })
  it('a pasted URL, a port, a trailing dot and case all still block', () => {
    expect(isBlocked('https://ex.com/a', ['https://ex.com/a'])).toBe(true)
    expect(isBlocked('https://ex.com/a/b', ['https://ex.com'])).toBe(true)
    expect(isBlocked('https://ex.com:8443/x', ['ex.com:8443'])).toBe(true)
    expect(isBlocked('https://ex.com/x', ['ex.com:8443'])).toBe(true)
    expect(isBlocked('https://ex.com/x', ['ex.com.'])).toBe(true)
    expect(isBlocked('https://ex.com./x', ['ex.com'])).toBe(true)
    expect(isBlocked('https://EX.com/x', ['Ex.Com'])).toBe(true)
    expect(isBlocked('http://ex.com/x?q=1#f', ['ex.com/x?z'])).toBe(true)
  })
  it('an international name equals its punycode', () => {
    expect(isBlocked('https://xn--bcher-kva.de/', ['bücher.de'])).toBe(true)
    expect(isBlocked('https://bücher.de/', ['xn--bcher-kva.de'])).toBe(true)
    expect(isBlocked('https://shop.bücher.de/', ['*.bücher.de'])).toBe(true)
  })
  it('matches a path prefix, also encoded, doubled-slash or as a hash route', () => {
    expect(isBlocked('https://github.com/helper1/x', ['github.com/helper1'])).toBe(true)
    expect(isBlocked('https://github.com/other/x', ['github.com/helper1'])).toBe(false)
    expect(isBlocked('https://github.com/helper1foo', ['github.com/helper1'])).toBe(false)
    expect(isBlocked('https://ex.com/%61dmin/x', ['ex.com/admin'])).toBe(true)
    expect(isBlocked('https://ex.com/%2561dmin', ['ex.com/admin'])).toBe(true)
    expect(isBlocked('https://ex.com//admin', ['ex.com/admin'])).toBe(true)
    expect(isBlocked('https://ex.com/ADMIN', ['ex.com/admin'])).toBe(true)
    expect(isBlocked('https://ex.com/app/../admin', ['ex.com/admin'])).toBe(true)
    expect(isBlocked('https://ex.com/#/records/4', ['ex.com/records'])).toBe(true)
    expect(isBlocked('https://ex.com/#/other', ['ex.com/records'])).toBe(false)
  })
  it('blocks nothing by default but a url it cannot parse', () => {
    expect(isBlocked('https://a.test/', [])).toBe(false)
    expect(isBlocked('not a url', [])).toBe(true)
  })
  it('flags a line it cannot read, and that line blocks everything', () => {
    expect(normalizePattern('ex.com')).toEqual({ host: 'ex.com', path: '' })
    expect(invalidPatterns(['ex.com', 'ex com', 'http://', '*'])).toEqual(['ex com', 'http://', '*'])
    expect(isBlocked('https://a.test/', ['ex com'])).toBe(true)
  })
  it('keeps only origin and path for the note', () => {
    expect(originPath('https://ex.com:8443/a/b?id=7#/p')).toBe('https://ex.com:8443/a/b')
    expect(originPath('garbage')).toBe('')
  })
})
