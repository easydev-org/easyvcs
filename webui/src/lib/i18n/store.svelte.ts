// Reactive language store + a typed `t()`. Language persists in localStorage and
// defaults to the browser language (zh* -> Chinese, else English).
import { translate, type Lang, type MessageKey } from './index'

const KEY = 'easyvcsLang'

function detect(): Lang {
  const saved = localStorage.getItem(KEY)
  if (saved === 'en' || saved === 'zh') return saved
  return navigator.language?.toLowerCase().startsWith('zh') ? 'zh' : 'en'
}

class I18n {
  lang = $state<Lang>(detect())
  set(l: Lang) {
    this.lang = l
    localStorage.setItem(KEY, l)
    document.documentElement.lang = l
  }
  // t('requests.mergeAnyway') / t('repos.created', { ns: 'team/app' })
  t = (key: MessageKey, vars?: Record<string, string | number>): string =>
    translate(this.lang, key, vars)
}

export const i18n = new I18n()
