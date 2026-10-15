// A tiny toast store: push(kind, message); auto-dismiss.
export type ToastKind = 'success' | 'error' | 'info'
export interface Toast {
  id: number
  kind: ToastKind
  message: string
}
let nextId = 1
class Toasts {
  items = $state<Toast[]>([])
  push(kind: ToastKind, message: string, ms = 3500) {
    const id = nextId++
    this.items = [...this.items, { id, kind, message }]
    setTimeout(() => this.dismiss(id), ms)
  }
  success(m: string) {
    this.push('success', m)
  }
  error(m: string) {
    this.push('error', m)
  }
  info(m: string) {
    this.push('info', m)
  }
  dismiss(id: number) {
    this.items = this.items.filter((t) => t.id !== id)
  }
}
export const toasts = new Toasts()
