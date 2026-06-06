// @vitest-environment jsdom
/**
 * Tests for AccountLedgerPanel — verifies data table render + total + empty state.
 */
import { render, screen, waitFor } from '@testing-library/react'
import '@testing-library/jest-dom'
import { vi, test, expect, afterEach } from 'vitest'
import { AccountLedgerPanel } from './AccountLedgerPanel'

afterEach(() => vi.restoreAllMocks())

function mockFetch(body: unknown) {
  vi.stubGlobal(
    'fetch',
    vi.fn(() => Promise.resolve({ ok: true, json: () => Promise.resolve(body) } as Response)),
  )
}

test('renders ledger rows and total cost', async () => {
  mockFetch({
    entries: [{ account: 'A1', model: 'opus', tokens_in: 1000, tokens_out: 2000, cost: 0.1234, period: '2026-W23' }],
    total_cost: 0.1234,
  })
  render(<AccountLedgerPanel />)
  await waitFor(() => expect(screen.getByText('A1')).toBeInTheDocument())
  expect(screen.getByText('$0.1234')).toBeInTheDocument()
})

test('renders empty state when no entries', async () => {
  mockFetch({ entries: [], total_cost: 0 })
  render(<AccountLedgerPanel />)
  await waitFor(() => expect(screen.getByText(/no usage recorded/i)).toBeInTheDocument())
})
