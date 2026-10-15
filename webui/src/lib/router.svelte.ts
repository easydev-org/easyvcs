// A tiny hash router. State lives in the URL (#/ns?ref=&path=&tab=) so links are
// shareable, refresh keeps the view, and back/forward work. Reactive via $state.
export interface Route {
  ns: string
  ref: string
  path: string
  tab: string
  iid: string // revision-request iid when viewing a request
}

function parse(): Route {
  // e.g. "#/easyvcs-app?ref=main&path=lib&tab=code"
  const raw = location.hash.replace(/^#\/?/, '')
  const [nsPart, query] = raw.split('?')
  const q = new URLSearchParams(query ?? '')
  return {
    ns: decodeURIComponent(nsPart ?? ''),
    ref: q.get('ref') ?? '',
    path: q.get('path') ?? '',
    tab: q.get('tab') ?? 'code',
    iid: q.get('iid') ?? '',
  }
}

class Router {
  route = $state<Route>(parse())
  private listening = false

  init() {
    if (this.listening) return
    this.listening = true
    addEventListener('hashchange', () => (this.route = parse()))
  }
  navigate(r: Partial<Route>) {
    const cur = parse()
    const next: Route = { ...cur, ...r }
    const q = new URLSearchParams()
    if (next.ref) q.set('ref', next.ref)
    if (next.path) q.set('path', next.path)
    if (next.tab && next.tab !== 'code') q.set('tab', next.tab)
    if (next.iid) q.set('iid', next.iid)
    const qs = q.toString()
    location.hash = '#/' + encodeURIComponent(next.ns) + (qs ? '?' + qs : '')
  }
}

export const router = new Router()
