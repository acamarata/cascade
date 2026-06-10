/**
 * Purpose: Renders assistant message content as GitHub-Flavored Markdown with
 *   syntax-highlighted code blocks (rehype-highlight / highlight.js) and a
 *   copy-to-clipboard button on each fenced code block.
 * Inputs: content string — raw markdown text from the assistant; may be a
 *   partial buffer during streaming.
 * Outputs: Styled markdown with headings, lists, inline code, fenced code
 *   blocks (with language-tagged syntax highlighting and Copy button), and
 *   links opening in a new tab.
 * Constraints: No LaTeX, no Mermaid (P4). Trust-mode rendering (local-only
 *   tool; no user-supplied external content; rehype-sanitize not required).
 *   Pure presentational — no state except the transient copy-success flash.
 * SPORT: T-P3-E02-22
 */
import { useState } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import rehypeHighlight from 'rehype-highlight'
import type { ComponentPropsWithoutRef } from 'react'
import { cn } from '@/lib/utils'

// Import highlight.js dark theme for syntax-highlighted code blocks.
import 'highlight.js/styles/github-dark.css'

// ─── CodeBlock ──────────────────────────────────────────────────────────────

interface CodeProps extends ComponentPropsWithoutRef<'code'> {
  inline?: boolean
}

/**
 * Purpose: Renders inline code as a monospace chip and block code as a
 *   dark-background <pre> with a Copy button.
 * Inputs: inline boolean (injected by react-markdown), className (language-*),
 *   children (code text).
 * Outputs: Styled <code> or <pre><code> element.
 * Constraints: Copy uses navigator.clipboard — only available on HTTPS/localhost.
 * SPORT: T-P3-E02-22
 */
function CodeBlock({ inline, className, children, ...rest }: CodeProps) {
  const [copied, setCopied] = useState(false)

  const text = String(children ?? '').replace(/\n$/, '')

  if (inline) {
    return (
      <code
        className="bg-muted px-1 rounded font-mono text-[0.85em]"
        {...rest}
      >
        {children}
      </code>
    )
  }

  function handleCopy() {
    navigator.clipboard.writeText(text).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    })
  }

  return (
    <div className="relative group my-2">
      <button
        onClick={handleCopy}
        className={cn(
          'absolute top-2 right-2 z-10 px-2 py-0.5 text-xs rounded',
          'bg-zinc-700 text-zinc-200 hover:bg-zinc-600 transition-colors',
          'opacity-0 group-hover:opacity-100 focus:opacity-100',
        )}
        aria-label="Copy code"
        type="button"
      >
        {copied ? 'Copied!' : 'Copy'}
      </button>
      <pre className="overflow-x-auto rounded bg-zinc-900 p-3 text-xs leading-relaxed">
        <code className={className} {...rest}>
          {children}
        </code>
      </pre>
    </div>
  )
}

// ─── MarkdownMessage ─────────────────────────────────────────────────────────

interface MarkdownMessageProps {
  /** Markdown string to render; may be partial during streaming. */
  content: string
}

export function MarkdownMessage({ content }: MarkdownMessageProps) {
  if (!content) return null

  return (
    <ReactMarkdown
      remarkPlugins={[remarkGfm]}
      rehypePlugins={[rehypeHighlight]}
      components={{
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        code: CodeBlock as any,
        // Headings
        h1: ({ children }) => (
          <h1 className="text-base font-bold mt-3 mb-1 first:mt-0">{children}</h1>
        ),
        h2: ({ children }) => (
          <h2 className="text-sm font-semibold mt-2 mb-1 first:mt-0">{children}</h2>
        ),
        h3: ({ children }) => (
          <h3 className="text-sm font-medium mt-2 mb-0.5 first:mt-0">{children}</h3>
        ),
        // Lists
        ul: ({ children }) => (
          <ul className="list-disc list-inside space-y-0.5 my-1 text-sm">{children}</ul>
        ),
        ol: ({ children }) => (
          <ol className="list-decimal list-inside space-y-0.5 my-1 text-sm">{children}</ol>
        ),
        li: ({ children }) => <li className="ml-2">{children}</li>,
        // Block elements
        p: ({ children }) => (
          <p className="text-sm leading-relaxed my-1 first:mt-0 last:mb-0">{children}</p>
        ),
        blockquote: ({ children }) => (
          <blockquote className="border-l-2 border-muted-foreground/40 pl-3 my-1 text-muted-foreground/80 italic text-sm">
            {children}
          </blockquote>
        ),
        // Links — open in new tab (local-only tool, trust mode)
        a: ({ href, children }) => (
          <a
            href={href}
            target="_blank"
            rel="noreferrer noopener"
            className="text-primary underline underline-offset-2 hover:text-primary/80"
          >
            {children}
          </a>
        ),
        // Horizontal rule
        hr: () => <hr className="my-2 border-muted-foreground/20" />,
        // Bold / italic (defaults handle these; ensure no extra margin)
        strong: ({ children }) => <strong className="font-semibold">{children}</strong>,
        em: ({ children }) => <em className="italic">{children}</em>,
        // Table (GFM)
        table: ({ children }) => (
          <div className="overflow-x-auto my-2">
            <table className="text-xs border-collapse w-full">{children}</table>
          </div>
        ),
        th: ({ children }) => (
          <th className="border border-muted-foreground/30 px-2 py-1 bg-muted font-medium text-left">
            {children}
          </th>
        ),
        td: ({ children }) => (
          <td className="border border-muted-foreground/30 px-2 py-1">{children}</td>
        ),
      }}
    >
      {content}
    </ReactMarkdown>
  )
}
