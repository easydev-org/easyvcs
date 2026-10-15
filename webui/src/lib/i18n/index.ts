// Minimal, STRONGLY-TYPED i18n. `en` is the schema (Dict); `zh` must match it
// exactly (compile-checked). `t('a.b', {x})` is type-safe: the key is a literal
// union of every dotted path, and the interpolation vars are typed per key.
import { en, type Dict } from './en'
import { zh } from './zh'

export type Lang = 'en' | 'zh'
export const dicts: Record<Lang, Dict> = { en, zh }

// A dotted key path into the (nested) dictionary, e.g. "requests.mergeAnyway".
type Join<K, P> = K extends string ? (P extends string ? `${K}.${P}` : never) : never
type Paths<T> = T extends object ? { [K in keyof T]-?: K extends string ? Join<K, Paths<T[K]>> | K : never }[keyof T] : never
export type MessageKey = Paths<Dict>

// The leaf value at a dotted path (a string template).
type ValueAt<T, P extends string> = P extends `${infer K}.${infer R}`
  ? K extends keyof T
    ? ValueAt<T[K], R>
    : never
  : P extends keyof T
    ? T[P]
    : never
export type Message<P extends MessageKey> = ValueAt<Dict, P> extends string ? ValueAt<Dict, P> : never

// Extract {var} names from a message template into an object type.
type Vars<S extends string> = S extends `${string}{${infer V}}${infer R}`
  ? { [K in V]: string | number } & Vars<R>
  : // eslint-disable-next-line @typescript-eslint/ban-types
    {}

function lookup(d: unknown, path: string): string | undefined {
  let cur: unknown = d
  for (const part of path.split('.')) {
    if (cur && typeof cur === 'object' && part in (cur as Record<string, unknown>)) {
      cur = (cur as Record<string, unknown>)[part]
    } else {
      return undefined
    }
  }
  return typeof cur === 'string' ? cur : undefined
}

export function translate(lang: Lang, key: MessageKey, vars?: Record<string, string | number>): string {
  const msg = lookup(dicts[lang], key) ?? lookup(dicts.en, key) ?? key
  if (!vars) return msg
  return msg.replace(/\{(\w+)\}/g, (_, k: string) => (k in vars ? String(vars[k]) : `{${k}}`))
}

export { en, zh }
export type { Dict }
