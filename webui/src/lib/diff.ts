// Unified-diff parsing + a tiny syntax highlighter, shared by the file view and
// the RR diff view. The server renders unified diffs (`@@` hunks, +/- lines);
// we parse them into typed lines so the UI can colour them like GitHub/GitLab.

export type DiffLineKind = 'add' | 'del' | 'hunk' | 'ctx'
export interface DiffLine {
  kind: DiffLineKind
  text: string
  // old/new line numbers when known (for a line-number gutter)
  oldNo?: number
  newNo?: number
}
export interface ParsedFile {
  path: string
  status: string
  binary: boolean
  added: number
  removed: number
  lines: DiffLine[]
}

// parseUnified turns one file's unified-diff text into typed lines. Lines the
// server emits are: "diff --git …" (ignored), "@@ -a,b +c,d @@" (hunk headers),
// "+…"/"-…" (changes), and " …" (context).
export function parseUnified(content: string, path: string, status: string, binary: boolean, added: number, removed: number): ParsedFile {
  const lines: DiffLine[] = []
  let oldNo = 0
  let newNo = 0
  if (!content) return { path, status, binary, added, removed, lines }
  for (const raw of content.split('\n')) {
    if (raw.startsWith('@@')) {
      const m = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(raw)
      if (m) {
        oldNo = parseInt(m[1], 10)
        newNo = parseInt(m[2], 10)
      }
      lines.push({ kind: 'hunk', text: raw })
    } else if (raw.startsWith('+++') || raw.startsWith('---') || raw.startsWith('diff ')) {
      // file headers — show as hunk-styled separators but do not count them
      lines.push({ kind: 'hunk', text: raw })
    } else if (raw.startsWith('+')) {
      lines.push({ kind: 'add', text: raw.slice(1), newNo: newNo++ })
    } else if (raw.startsWith('-')) {
      lines.push({ kind: 'del', text: raw.slice(1), oldNo: oldNo++ })
    } else if (raw.startsWith(' ')) {
      lines.push({ kind: 'ctx', text: raw.slice(1), oldNo: oldNo++, newNo: newNo++ })
    } else if (raw.length > 0) {
      lines.push({ kind: 'ctx', text: raw, oldNo: oldNo++, newNo: newNo++ })
    }
  }
  return { path, status, binary, added, removed, lines }
}

// highlight tokenizes a single line into HTML-escaped spans by language. It is
// deliberately small (comments, strings, numbers, keywords) — enough to make
// code readable without shipping a full grammar set.
const KEYWORDS =
  /\b(func|package|import|return|if|else|for|range|var|const|type|struct|interface|map|chan|go|defer|select|case|switch|break|continue|nil|true|false|def|class|self|from|as|with|try|except|finally|yield|lambda|pass|raise|while|elif|and|or|not|in|is|new|this|void|int|string|bool|float|double|public|private|protected|static|final|extends|implements|export|default|async|await|let|const|function|of|null|undefined|None|True|False)\b/

function esc(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
}

// highlight returns HTML for one code line: comments grey, strings green,
// numbers amber, keywords purple. Everything is escaped first.
export function highlight(line: string, ext: string): string {
  // Whole-line comment.
  const c = commentPrefix(ext)
  const trimmed = line.trimStart()
  if (c && trimmed.startsWith(c)) return `<span class="text-muted-foreground italic">${esc(line)}</span>`
  let out = ''
  let i = 0
  const re = /("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|`(?:[^`\\]|\\.)*`|\b\d[\d._]*\b|\b[A-Za-z_]\w*\b)/g
  let m: RegExpExecArray | null
  while ((m = re.exec(line)) !== null) {
    out += esc(line.slice(i, m.index))
    const tok = m[0]
    if (tok[0] === '"' || tok[0] === "'" || tok[0] === '`') out += `<span class="text-success">${esc(tok)}</span>`
    else if (/^\d/.test(tok)) out += `<span class="text-warning">${esc(tok)}</span>`
    else if (KEYWORDS.test(tok)) out += `<span class="text-accent">${esc(tok)}</span>`
    else out += esc(tok)
    i = m.index + tok.length
  }
  out += esc(line.slice(i))
  return out
}

function commentPrefix(ext: string): string {
  switch (ext) {
    case 'go':
    case 'js':
    case 'ts':
    case 'java':
    case 'c':
    case 'h':
    case 'cc':
    case 'cpp':
    case 'rs':
      return '//'
    case 'py':
    case 'sh':
    case 'yaml':
    case 'yml':
      return '#'
    default:
      return ''
  }
}

export function extOf(path: string): string {
  const i = path.lastIndexOf('.')
  return i < 0 ? '' : path.slice(i + 1).toLowerCase()
}
