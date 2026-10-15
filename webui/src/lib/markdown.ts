// A tiny, dependency-free Markdown renderer for READMEs / comments / RR bodies.
// It escapes HTML first (no raw HTML passthrough), then applies a small subset:
// headings, bold/italic/code, links, lists, blockquotes, hr, fenced code, and
// paragraphs. Good enough for readable docs without shipping a full parser.
function esc(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
}
function inline(s: string): string {
  return s
    .replace(/`([^`]+)`/g, '<code class="rounded bg-secondary px-1 py-0.5 font-mono text-[0.9em]">$1</code>')
    .replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>')
    .replace(/\*([^*]+)\*/g, '<em>$1</em>')
    .replace(/\[([^\]]+)\]\(([^)]+)\)/g, '<a class="text-primary hover:underline" href="$2" rel="noopener noreferrer">$1</a>')
}
export function renderMarkdown(src: string): string {
  const lines = esc(src).split('\n')
  const out: string[] = []
  let inCode = false
  let list: 'ul' | 'ol' | null = null
  const closeList = () => {
    if (list) {
      out.push(`</${list}>`)
      list = null
    }
  }
  for (const raw of lines) {
    const line = raw
    if (line.startsWith('```')) {
      if (inCode) {
        out.push('</code></pre>')
        inCode = false
      } else {
        closeList()
        out.push('<pre class="overflow-auto rounded-md bg-secondary p-3 font-mono text-micro leading-relaxed"><code>')
        inCode = true
      }
      continue
    }
    if (inCode) {
      out.push(line)
      continue
    }
    const h = /^(#{1,6})\s+(.*)$/.exec(line)
    if (h) {
      closeList()
      const lvl = h[1].length
      const sizes = ['text-body font-semibold', 'text-body font-semibold', 'text-meta font-semibold', 'text-meta font-medium', 'text-micro font-medium', 'text-micro font-medium']
      out.push(`<h${lvl} class="${sizes[lvl - 1]} mt-4 mb-2">${inline(h[2])}</h${lvl}>`)
      continue
    }
    if (/^\s*[-*]\s+/.test(line)) {
      if (list !== 'ul') {
        closeList()
        out.push('<ul class="my-2 list-disc pl-5">')
        list = 'ul'
      }
      out.push(`<li>${inline(line.replace(/^\s*[-*]\s+/, ''))}</li>`)
      continue
    }
    if (/^\s*\d+\.\s+/.test(line)) {
      if (list !== 'ol') {
        closeList()
        out.push('<ol class="my-2 list-decimal pl-5">')
        list = 'ol'
      }
      out.push(`<li>${inline(line.replace(/^\s*\d+\.\s+/, ''))}</li>`)
      continue
    }
    if (/^\s*>\s?/.test(line)) {
      closeList()
      out.push(`<blockquote class="my-2 border-l-2 border-border pl-3 text-muted-foreground">${inline(line.replace(/^\s*>\s?/, ''))}</blockquote>`)
      continue
    }
    if (/^\s*---+\s*$/.test(line)) {
      closeList()
      out.push('<hr class="my-4 border-border" />')
      continue
    }
    if (line.trim() === '') {
      closeList()
      continue
    }
    closeList()
    out.push(`<p class="my-2">${inline(line)}</p>`)
  }
  if (inCode) out.push('</code></pre>')
  closeList()
  return out.join('\n')
}
