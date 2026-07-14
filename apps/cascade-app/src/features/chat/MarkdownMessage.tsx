/**
 * Purpose: Renders assistant message content as GitHub-Flavored Markdown with
 *   syntax-highlighted code blocks (rehype-highlight) and copy-to-clipboard
 *   on fenced code blocks. Ported from cascade-dashboard GPChatPanel T-P3-E02-22.
 * Inputs: content string — raw markdown; may be partial during streaming.
 * Outputs: Styled markdown: headings, lists, inline code, fenced blocks (language
 *   tagged, syntax highlighted, Copy button), GFM tables, links new-tab.
 * Constraints: Trust-mode rendering (local-only tool; no external user content;
 *   rehype-sanitize not required). Pure presentational — no state except copy flash.
 * SPORT: E-P9-03 in-app chat — MarkdownMessage
 */
import { useState } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import rehypeHighlight from 'rehype-highlight'
import type { ComponentPropsWithoutRef } from 'react'
import { cn } from '@/lib/utils'

// Syntax-highlighted code block theme
import 'highlight.js/styles/github-dark.css'

// ─── CodeBlock ────────────────────────────────────────────────────────────────

interface CodeProps extends ComponentPropsWithoutRef<'code'> {
  inline?: boolean
}

/**
 * Purpose: Inline code chip and block code with dark background + Copy button.
 * Inputs: inline boolean (from react-markdown), className (language-*), children.
 * Outputs: Styled <code> or <pre><code> element.
 * Constraints: Copy uses navigator.clipboard — only on HTTPS/localhost.
 * SPORT: E-P9-03 in-app chat — CodeBlock
 */
function CodeBlock({ inline, className, children, ...rest }: CodeProps) {
  const [copied, setCopied] = useState(false)

  const text = String(children ?? '').replace(/\n$/, '')

  if (inline) {
    return (
      <code className="bg-muted px-1 rounded font-mono text-[0.85em]" {...rest}>
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
          'opacity-0 group-hover:opacity-100 focus:opacity-100'
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

// ─── MarkdownMessage ──────────────────────────────────────────────────────────

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
        h1: ({ children }) => (
          <h1 className="text-base font-bold mt-3 mb-1 first:mt-0">{children}</h1>
        ),
        h2: ({ children }) => (
          <h2 className="text-sm font-semibold mt-2 mb-1 first:mt-0">{children}</h2>
        ),
        h3: ({ children }) => (
          <h3 className="text-sm font-medium mt-2 mb-0.5 first:mt-0">{children}</h3>
        ),
        ul: ({ children }) => (
          <ul className="list-disc list-inside space-y-0.5 my-1 text-sm">{children}</ul>
        ),
        ol: ({ children }) => (
          <ol className="list-decimal list-inside space-y-0.5 my-1 text-sm">{children}</ol>
        ),
        li: ({ children }) => <li className="ml-2">{children}</li>,
        p: ({ children }) => (
          <p className="text-sm leading-relaxed my-1 first:mt-0 last:mb-0">{children}</p>
        ),
        blockquote: ({ children }) => (
          <blockquote className="border-l-2 border-muted-foreground/40 pl-3 my-1 text-muted-foreground/80 italic text-sm">
            {children}
          </blockquote>
        ),
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
        hr: () => <hr className="my-2 border-muted-foreground/20" />,
        strong: ({ children }) => <strong className="font-semibold">{children}</strong>,
        em: ({ children }) => <em className="italic">{children}</em>,
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
