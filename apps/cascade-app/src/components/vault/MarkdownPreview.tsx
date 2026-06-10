/**
 * Purpose: Live preview pane for the vault editor — renders markdown content
 *   as sanitised HTML using the remark/rehype pipeline with GFM support and
 *   syntax highlighting. Updates within 300ms of editor changes via a debounced
 *   content prop.
 * Inputs:
 *   - content: raw markdown string from VaultContext / MarkdownEditor
 *   - className: optional extra Tailwind classes for the wrapper
 * Outputs:
 *   - Renders sanitised HTML into a scrollable article pane.
 *   - Script tags and other dangerous elements are stripped by rehype-sanitize.
 * Constraints:
 *   - remark processor instance is memoised at module level — never recreated per render.
 *   - content is debounced 300ms internally so fast typing does not block the UI thread.
 *   - dangerouslySetInnerHTML is used only after rehype-sanitize runs.
 *   - Scroll sync: editor → preview via data-line-N attributes (best-effort).
 * SPORT: MASTER-COMPONENTS.md — MarkdownPreview (T-P3-E06-04)
 */

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { unified } from 'unified'
import remarkParse from 'remark-parse'
import remarkGfm from 'remark-gfm'
import remarkRehype from 'remark-rehype'
import rehypeSanitize from 'rehype-sanitize'
import rehypeHighlight from 'rehype-highlight'
import rehypeStringify from 'rehype-stringify'

// ---------------------------------------------------------------------------
// Processor (module-level singleton — never recreated)
// ---------------------------------------------------------------------------

/**
 * remark → rehype pipeline with GFM, syntax highlighting, and sanitization.
 * Memoised at module scope to avoid rebuilding the pipeline on every render.
 */
const processor = unified()
  .use(remarkParse)
  .use(remarkGfm)
  .use(remarkRehype)
  .use(rehypeHighlight, { detect: true, ignoreMissing: true })
  .use(rehypeSanitize)
  .use(rehypeStringify)

/** Synchronous markdown → sanitised HTML string. */
export function renderMarkdown(content: string): string {
  const result = processor.processSync(content)
  return String(result)
}

// ---------------------------------------------------------------------------
// Debounce hook
// ---------------------------------------------------------------------------

/** Returns a debounced copy of the value, updated after `delay` ms of silence. */
function useDebounced<T>(value: T, delay: number): T {
  const [debounced, setDebounced] = useState(value)
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(() => {
    if (timer.current) clearTimeout(timer.current)
    timer.current = setTimeout(() => setDebounced(value), delay)
    return () => {
      if (timer.current) clearTimeout(timer.current)
    }
  }, [value, delay])

  return debounced
}

// ---------------------------------------------------------------------------
// Props
// ---------------------------------------------------------------------------

export interface MarkdownPreviewProps {
  /** Raw markdown string. Debounced internally at 300ms before re-rendering. */
  content: string
  /** Optional extra classes for the outer wrapper div. */
  className?: string
  /** Ref forwarded to the scrollable article element (for scroll-sync). */
  scrollRef?: React.RefObject<HTMLElement | null>
}

// ---------------------------------------------------------------------------
// MarkdownPreview component
// ---------------------------------------------------------------------------

/**
 * Live markdown preview pane.
 * Renders GFM-flavoured markdown as sanitised, syntax-highlighted HTML.
 * Content updates are debounced at 300ms to avoid blocking fast typists.
 */
export function MarkdownPreview({ content, className = '', scrollRef }: MarkdownPreviewProps) {
  const debouncedContent = useDebounced(content, 300)

  const html = useMemo(() => {
    if (!debouncedContent) return ''
    try {
      return renderMarkdown(debouncedContent)
    } catch {
      return ''
    }
  }, [debouncedContent])

  const articleRef = useCallback(
    (node: HTMLElement | null) => {
      if (scrollRef && 'current' in scrollRef) {
        // @ts-expect-error – assigning to a ref passed by the caller
        scrollRef.current = node
      }
    },
    [scrollRef]
  )

  if (!content) {
    return (
      <div
        className={`flex h-full items-center justify-center text-sm text-muted-foreground ${className}`}
        data-testid="markdown-preview-empty"
      >
        Nothing to preview
      </div>
    )
  }

  return (
    <div className={`h-full overflow-y-auto ${className}`} data-testid="markdown-preview-scroll">
      <article
        ref={articleRef}
        // rehype-sanitize strips <script> and other dangerous elements before
        // this dangerouslySetInnerHTML call — the HTML is safe.
        // eslint-disable-next-line react/no-danger
        dangerouslySetInnerHTML={{ __html: html }}
        className="prose prose-sm dark:prose-invert max-w-none px-6 py-4"
        aria-label="Markdown preview"
        data-testid="markdown-preview"
      />
    </div>
  )
}
