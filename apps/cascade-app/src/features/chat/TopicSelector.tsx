/**
 * Purpose: "View all topics" control for the Personal chat scope. Fetches
 *   the daemon's topic list and lets the user focus Personal chat on one
 *   topic (or reset to "View all topics").
 * Inputs: selectedTopicId (string | null; null = view all), onSelect callback.
 * Outputs: Select dropdown populated from GET http://127.0.0.1:9761/api/topics
 *   (contract: `{ topics: [{ id, title }] }`). Falls back to "View all
 *   topics" only when the daemon is unreachable or returns no topics —
 *   never throws, never blocks the Personal chat from working.
 * Constraints: Personal scope only (wired in ChatPage.tsx). Does not persist
 *   selection itself — the caller (ChatPage) owns selectedTopicId via the
 *   chatScope store slice.
 * SPORT: E-P9-03 in-app chat — TopicSelector
 */
import { useEffect, useState } from 'react'
import { ListTree } from 'lucide-react'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

const TOPICS_URL = 'http://127.0.0.1:9761/api/topics'

interface Topic {
  id: string
  title: string
}

interface TopicsResponse {
  topics?: Topic[]
}

interface TopicSelectorProps {
  /** null = view all topics (no focus). */
  selectedTopicId: string | null
  onSelect: (topicId: string | null) => void
}

/** Sentinel value for the Radix Select item representing "view all" (Radix disallows an empty-string item value). */
const ALL_TOPICS_VALUE = '__all__'

export function TopicSelector({ selectedTopicId, onSelect }: TopicSelectorProps) {
  const [topics, setTopics] = useState<Topic[]>([])
  const [status, setStatus] = useState<'loading' | 'ready' | 'error'>('loading')

  useEffect(() => {
    let cancelled = false
    fetch(TOPICS_URL)
      .then((res) => {
        if (!res.ok) throw new Error(`HTTP ${res.status}`)
        return res.json() as Promise<TopicsResponse>
      })
      .then((data) => {
        if (cancelled) return
        setTopics(Array.isArray(data?.topics) ? data.topics : [])
        setStatus('ready')
      })
      .catch(() => {
        if (cancelled) return
        setTopics([])
        setStatus('error')
      })
    return () => {
      cancelled = true
    }
  }, [])

  function handleChange(value: string) {
    onSelect(value === ALL_TOPICS_VALUE ? null : value)
  }

  // Daemon unreachable — degrade gracefully rather than block Personal chat.
  if (status === 'error') {
    return (
      <span
        className="text-[0.65rem] text-muted-foreground/60"
        title="Could not reach the Cascade daemon topics endpoint (127.0.0.1:9761)"
      >
        Topics unavailable
      </span>
    )
  }

  return (
    <Select value={selectedTopicId ?? ALL_TOPICS_VALUE} onValueChange={handleChange}>
      <SelectTrigger
        className="h-7 text-xs w-44 shrink-0"
        aria-label="Focus Personal chat on a topic"
      >
        <ListTree className="h-3 w-3 mr-1 shrink-0" aria-hidden="true" />
        <SelectValue placeholder="View all topics" />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value={ALL_TOPICS_VALUE} className="text-xs">
          View all topics
        </SelectItem>
        {status === 'ready' && topics.length === 0 && (
          <SelectItem value="__none__" disabled className="text-xs text-muted-foreground">
            No topics yet
          </SelectItem>
        )}
        {topics.map((t) => (
          <SelectItem key={t.id} value={t.id} className="text-xs">
            {t.title || t.id}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}
