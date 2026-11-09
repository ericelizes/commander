// The postMessage bridge between a Manager-authored HTML page (in a sandboxed,
// opaque-origin iframe) and the app. The page gets exactly three calls:
// window.cmdr.submitDecision({choice, text}), .copy(text) and .openUrl(url).

export const BRIDGE_TAG = 'cmdr-bridge'

export type BridgeMethod = 'submitDecision' | 'copy' | 'openUrl'

export interface BridgeRequest {
  id: number | string
  method: BridgeMethod
  params: Record<string, unknown>
}

export interface BridgeReply {
  [BRIDGE_TAG]: 1
  id: number | string
  ok: boolean
  error?: string
}

export interface BridgeHandlers {
  submitDecision(p: { choice: string, text: string }): Promise<void>
  copy(text: string): Promise<void>
  openUrl(url: string): void | Promise<void>
}

const METHODS: readonly BridgeMethod[] = ['submitDecision', 'copy', 'openUrl']
const MAX_CHOICE = 200
const MAX_TEXT = 4000
const MAX_COPY = 100_000

// A well-formed request, or null for anything else (ignored silently).
export function parseRequest(data: unknown): BridgeRequest | null {
  if (!data || typeof data !== 'object') return null
  const d = data as Record<string, unknown>
  if (d[BRIDGE_TAG] !== 1) return null
  if (typeof d.id !== 'number' && typeof d.id !== 'string') return null
  if (!METHODS.includes(d.method as BridgeMethod)) return null
  const params = d.params && typeof d.params === 'object' ? d.params as Record<string, unknown> : {}
  return { id: d.id, method: d.method as BridgeMethod, params }
}

export function safeUrl(raw: unknown): string | null {
  if (typeof raw !== 'string') return null
  try {
    const u = new URL(raw)
    return u.protocol === 'https:' || u.protocol === 'http:' ? u.href : null
  } catch {
    return null
  }
}

// Validates params and runs the handler; never throws.
export async function handleBridgeRequest(req: BridgeRequest, h: BridgeHandlers): Promise<BridgeReply> {
  const reply = (ok: boolean, error?: string): BridgeReply => ({ [BRIDGE_TAG]: 1, id: req.id, ok, ...(error ? { error } : {}) })
  try {
    switch (req.method) {
      case 'submitDecision': {
        const choice = typeof req.params.choice === 'string' ? req.params.choice.trim() : ''
        const text = typeof req.params.text === 'string' ? req.params.text.trim() : ''
        if (!choice && !text) return reply(false, 'submitDecision needs a choice and/or text')
        if (choice.length > MAX_CHOICE || text.length > MAX_TEXT) return reply(false, 'answer too long')
        await h.submitDecision({ choice, text })
        return reply(true)
      }
      case 'copy': {
        const text = req.params.text
        if (typeof text !== 'string' || text.length > MAX_COPY) return reply(false, 'copy needs text')
        await h.copy(text)
        return reply(true)
      }
      case 'openUrl': {
        const url = safeUrl(req.params.url)
        if (!url) return reply(false, 'only http(s) URLs can be opened')
        await h.openUrl(url)
        return reply(true)
      }
    }
  } catch (err) {
    return reply(false, err instanceof Error ? err.message : String(err))
  }
}

// The page-side half: defines window.cmdr and routes http(s) link clicks to
// openUrl (the sandbox blocks popups and top navigation).
export const BRIDGE_CLIENT = `(function(){
var n=0,pending={};
window.addEventListener('message',function(e){
  var d=e.data;
  if(e.source!==window.parent||!d||d['${BRIDGE_TAG}']!==1||!(d.id in pending))return;
  var p=pending[d.id];delete pending[d.id];
  d.ok?p[0]():p[1](new Error(d.error||'failed'));
});
function call(method,params){return new Promise(function(res,rej){
  var id=++n;pending[id]=[res,rej];
  window.parent.postMessage({'${BRIDGE_TAG}':1,id:id,method:method,params:params},'*');
});}
window.cmdr=Object.freeze({
  submitDecision:function(a){a=a||{};return call('submitDecision',{choice:String(a.choice||''),text:String(a.text||'')});},
  copy:function(t){return call('copy',{text:String(t)});},
  openUrl:function(u){return call('openUrl',{url:String(u)});}
});
document.addEventListener('click',function(e){
  var a=e.target&&e.target.closest&&e.target.closest('a[href]');
  if(a&&/^https?:/i.test(a.href)){e.preventDefault();window.cmdr.openUrl(a.href);}
});
})();`

// The CSP every html tab runs under: no network (connect/frames/forms), inline
// styles and images only, inline scripts only when the tab asks for them.
export function contentSecurityPolicy(allowScripts: boolean): string {
  return [
    "default-src 'none'",
    "img-src data: blob: https:",
    "style-src 'unsafe-inline'",
    "font-src data:",
    `script-src ${allowScripts ? "'unsafe-inline'" : "'none'"}`,
    "connect-src 'none'",
    "frame-src 'none'",
    "form-action 'none'",
    "base-uri 'none'",
  ].join('; ')
}

// The srcdoc for a page: our CSP (and bridge, when scripts run) go first in
// <head>, so the page cannot loosen them (CSPs only ever combine stricter).
export function buildSrcdoc(html: string, allowScripts: boolean): string {
  const csp = contentSecurityPolicy(allowScripts).replace(/"/g, '&quot;')
  let inject = `<meta http-equiv="Content-Security-Policy" content="${csp}">`
  if (allowScripts) inject += `<script>${BRIDGE_CLIENT}</script>`
  const head = /<head(\s[^>]*)?>/i.exec(html)
  if (head) return splice(html, head.index + head[0].length, inject)
  const root = /<html(\s[^>]*)?>/i.exec(html)
  if (root) return splice(html, root.index + root[0].length, `<head>${inject}</head>`)
  const doctype = /<!doctype[^>]*>/i.exec(html)
  if (doctype) return splice(html, doctype.index + doctype[0].length, `<head>${inject}</head>`)
  return `<!doctype html><head>${inject}</head>${html}`
}

function splice(s: string, at: number, insert: string): string {
  return s.slice(0, at) + insert + s.slice(at)
}
