// Which pages the app never captures for the agent. The user fills a list of
// patterns (Settings > Browser capture); a tab whose URL matches one is never
// captured, by the renderer (no request) and again by Electron main.
// Host-level blocks (`private.example.com`) are the primary, reliable form; a path
// block (`host/admin`) is best effort (a site can serve the same page under
// other paths), so block the host for anything sensitive.

/** Parses the textarea: one pattern per line, blank lines and `#` comments dropped. */
export function parsePatterns(text: string): string[] {
  return text
    .split('\n')
    .map((l) => l.replace(/#.*$/, '').trim().toLowerCase())
    .filter(Boolean)
}

export type Pattern = { host: string; path: string }

/** A path in one comparable form: percent-decoded (repeatedly), lower case, slashes collapsed, no trailing slash. */
function normPath(p: string): string {
  let s = p
  for (let i = 0; i < 3; i++) {
    try {
      const d = decodeURIComponent(s)
      if (d === s) break
      s = d
    } catch {
      break
    }
  }
  s = s.toLowerCase().replace(/\/{2,}/g, '/')
  return s.length > 1 ? s.replace(/\/$/, '') : s === '/' ? '' : s
}

const cleanHost = (h: string) => h.toLowerCase().replace(/\.+$/, '')

/**
 * normalizePattern reads one line the way a pasted URL would be: a scheme, a
 * port, a query or fragment, case, a trailing dot and a non-ASCII host (turned
 * to punycode) all fold away. Null = not a usable pattern.
 */
export function normalizePattern(raw: string): Pattern | null {
  let s = raw.trim().replace(/^[a-z][a-z0-9+.-]*:\/\//i, '').replace(/^\*\./, '')
  if (!s || s.startsWith('*')) return null
  try {
    const u = new URL(`https://${s}`)
    const host = cleanHost(u.hostname)
    if (!host || u.username || u.password) return null
    return { host, path: normPath(u.pathname) }
  } catch {
    return null
  }
}

/** The lines of the list that are not a usable pattern (shown in Settings; they block everything until fixed). */
export function invalidPatterns(patterns: string[]): string[] {
  return patterns.filter((p) => normalizePattern(p) === null)
}

/**
 * A pattern is a host (`app.example.com`, also its subdomains and any port),
 * `*.example.com` (subdomains and the domain itself), or `host/path` (that host
 * and a path prefix, also matched against a `#/route` fragment). A URL that
 * cannot be parsed, or a pattern line that cannot be read, counts as blocked:
 * nothing is captured when we cannot be sure.
 */
export function isBlocked(url: string, patterns: string[]): boolean {
  let u: URL
  try {
    u = new URL(url)
  } catch {
    return true
  }
  const host = cleanHost(u.hostname)
  if (!host) return true
  const paths = [normPath(u.pathname)]
  if (u.hash.startsWith('#/')) paths.push(normPath(u.hash.slice(1).replace(/[?].*$/, '')))
  return patterns.some((raw) => {
    const p = normalizePattern(raw)
    if (!p) return true
    if (host !== p.host && !host.endsWith(`.${p.host}`)) return false
    return !p.path || paths.some((x) => x === p.path || x.startsWith(`${p.path}/`))
  })
}

/** origin + path of a URL, nothing else (what the agent's note may say). */
export function originPath(url: string): string {
  try {
    const u = new URL(url)
    return u.origin === 'null' ? '' : `${u.origin}${u.pathname}`
  } catch {
    return ''
  }
}
