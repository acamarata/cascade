/**
 * pewsTree.test.ts — Vitest unit tests for the pure PEWS data helpers.
 *
 * Tests: tree helpers (flattenTickets, findTicketById, findPhaseById,
 *   ticketStatusCounts), status color mapping, mutation reducer (optimistic +
 *   reconcile), sync-event handler (mergeSyncEvent), and depends_on edge model.
 *
 * No React or Tauri — fully synchronous pure-function tests.
 * E-P8-05 | SPORT: MASTER-TESTS.md
 */

import { describe, it, expect } from 'vitest'
import {
  ticketStatusColor,
  phaseStatusColor,
  TICKET_STATUS_COLORS,
  PHASE_STATUS_COLORS,
  classifyAllPhases,
  buildDependsOnEdges,
  applyOptimisticStatusChange,
  reconcileOnError,
  mergeSyncEvent,
  flattenTickets,
  ticketStatusCounts,
  findTicketById,
  findPhaseById,
} from './pewsTree'
import type {
  PewsTree,
  TicketView,
  SprintView,
  WaveView,
  EpicView,
  PhaseView,
} from '../../types/pbd'

// ── Fixture builders ──────────────────────────────────────────────────────────

function makeTicket(overrides: Partial<TicketView> = {}): TicketView {
  return {
    id: 'T-001',
    sprintId: 'S-01',
    title: 'Test ticket',
    status: 'planned',
    dependsOn: [],
    repo: null,
    weight: null,
    note: null,
    blockedReason: null,
    ...overrides,
  }
}

function makeSprint(overrides: Partial<SprintView> & { tickets?: TicketView[] } = {}): SprintView {
  return {
    id: 'S-01',
    waveId: 'W-01',
    title: 'Sprint 1',
    status: 'queued',
    tickets: overrides.tickets ?? [makeTicket()],
    note: null,
    ...overrides,
  }
}

function makeWave(overrides: Partial<WaveView> & { sprints?: SprintView[] } = {}): WaveView {
  return {
    id: 'W-01',
    epicId: 'E-01',
    title: 'Wave 1',
    status: 'queued',
    sprints: overrides.sprints ?? [makeSprint()],
    note: null,
    ...overrides,
  }
}

function makeEpic(overrides: Partial<EpicView> & { waves?: WaveView[] } = {}): EpicView {
  return {
    id: 'E-01',
    phaseId: 'P-01',
    title: 'Epic 1',
    status: 'planned',
    dependsOn: [],
    waves: overrides.waves ?? [makeWave()],
    note: null,
    ...overrides,
  }
}

function makePhase(overrides: Partial<PhaseView> & { epics?: EpicView[] } = {}): PhaseView {
  return {
    id: 'P-01',
    title: 'Phase 1',
    status: 'building',
    epics: overrides.epics ?? [makeEpic()],
    note: null,
    ...overrides,
  }
}

function makeTree(overrides: Partial<PewsTree> = {}): PewsTree {
  return {
    phases: [makePhase()],
    currentPhase: 'P-01',
    currentEpic: 'E-01',
    currentWave: 'W-01',
    currentSprint: 'S-01',
    activeTickets: ['T-001'],
    ...overrides,
  }
}

// ── Status color mapping ──────────────────────────────────────────────────────

describe('ticketStatusColor', () => {
  it('returns the correct color for each known status', () => {
    expect(ticketStatusColor('planned')).toBe(TICKET_STATUS_COLORS['planned'])
    expect(ticketStatusColor('active')).toBe(TICKET_STATUS_COLORS['active'])
    expect(ticketStatusColor('done')).toBe(TICKET_STATUS_COLORS['done'])
    expect(ticketStatusColor('blocked')).toBe(TICKET_STATUS_COLORS['blocked'])
    expect(ticketStatusColor('archived')).toBe(TICKET_STATUS_COLORS['archived'])
  })

  it('falls back to gray for an unknown status', () => {
    expect(ticketStatusColor('unknown_status')).toBe('#6b7280')
  })

  it('all known statuses have a non-empty color', () => {
    const statuses = ['planned', 'queue', 'active', 'review', 'blocked', 'done', 'archived']
    for (const s of statuses) {
      const color = ticketStatusColor(s)
      expect(color).toBeTruthy()
      expect(color).toMatch(/^#[0-9a-f]{6}$/i)
    }
  })
})

describe('phaseStatusColor', () => {
  it('returns the correct color for building status', () => {
    expect(phaseStatusColor('building')).toBe(PHASE_STATUS_COLORS['building'])
  })

  it('falls back to gray for an unknown phase status', () => {
    expect(phaseStatusColor('unknown')).toBe('#6b7280')
  })

  it('all known phase statuses have a color', () => {
    const statuses = ['planning', 'ready_to_build', 'building', 'qa', 'shipped', 'archived']
    for (const s of statuses) {
      const color = phaseStatusColor(s)
      expect(color).toBeTruthy()
      expect(color).toMatch(/^#[0-9a-f]{6}$/i)
    }
  })
})

// ── Temporal classification ───────────────────────────────────────────────────

describe('classifyAllPhases', () => {
  it('classifies the current phase as "current"', () => {
    const tree = makeTree({ currentPhase: 'P-01' })
    const map = classifyAllPhases(tree)
    expect(map.get('P-01')).toBe('current')
  })

  it('classifies a shipped phase as "past"', () => {
    const past = makePhase({ id: 'P-00', title: 'Phase 0', status: 'shipped' })
    const tree = makeTree({
      phases: [past, makePhase()],
      currentPhase: 'P-01',
    })
    const map = classifyAllPhases(tree)
    expect(map.get('P-00')).toBe('past')
    expect(map.get('P-01')).toBe('current')
  })

  it('classifies an archived phase as "past"', () => {
    const past = makePhase({ id: 'P-A', title: 'Archived', status: 'archived' })
    const tree = makeTree({
      phases: [past, makePhase()],
      currentPhase: 'P-01',
    })
    const map = classifyAllPhases(tree)
    expect(map.get('P-A')).toBe('past')
  })

  it('classifies a planning phase as "future"', () => {
    const future = makePhase({ id: 'P-02', title: 'Phase 2', status: 'planning' })
    const tree = makeTree({
      phases: [makePhase(), future],
      currentPhase: 'P-01',
    })
    const map = classifyAllPhases(tree)
    expect(map.get('P-02')).toBe('future')
  })

  it('classifies ready_to_build as "future"', () => {
    const future = makePhase({ id: 'P-03', title: 'Phase 3', status: 'ready_to_build' })
    const tree = makeTree({
      phases: [makePhase(), future],
      currentPhase: 'P-01',
    })
    const map = classifyAllPhases(tree)
    expect(map.get('P-03')).toBe('future')
  })

  it('returns an empty map for an empty tree', () => {
    const tree = makeTree({ phases: [], currentPhase: null })
    const map = classifyAllPhases(tree)
    expect(map.size).toBe(0)
  })
})

// ── depends_on edge model ─────────────────────────────────────────────────────

describe('buildDependsOnEdges', () => {
  it('returns empty array when no tickets have dependsOn', () => {
    const tree = makeTree()
    const edges = buildDependsOnEdges(tree)
    expect(edges).toHaveLength(0)
  })

  it('builds an edge from the dependency to the dependent', () => {
    const dep = makeTicket({ id: 'T-001', title: 'Dependency ticket' })
    const dependent = makeTicket({ id: 'T-002', title: 'Dependent ticket', dependsOn: ['T-001'] })
    const tree = makeTree({
      phases: [
        makePhase({
          epics: [
            makeEpic({
              waves: [
                makeWave({
                  sprints: [makeSprint({ tickets: [dep, dependent] })],
                }),
              ],
            }),
          ],
        }),
      ],
    })

    const edges = buildDependsOnEdges(tree)
    expect(edges).toHaveLength(1)
    expect(edges[0]).toMatchObject({
      fromId: 'T-001',
      toId: 'T-002',
      fromTitle: 'Dependency ticket',
      toTitle: 'Dependent ticket',
    })
  })

  it('uses the dep id as fromTitle when the dep ticket is not in the tree', () => {
    const dependent = makeTicket({
      id: 'T-999',
      title: 'Orphan dependent',
      dependsOn: ['T-EXTERNAL'],
    })
    const tree = makeTree({
      phases: [
        makePhase({
          epics: [
            makeEpic({
              waves: [makeWave({ sprints: [makeSprint({ tickets: [dependent] })] })],
            }),
          ],
        }),
      ],
    })

    const edges = buildDependsOnEdges(tree)
    expect(edges).toHaveLength(1)
    expect(edges[0]!.fromTitle).toBe('T-EXTERNAL')
  })

  it('builds multiple edges for multiple depends_on entries', () => {
    const t1 = makeTicket({ id: 'T-A', title: 'A' })
    const t2 = makeTicket({ id: 'T-B', title: 'B' })
    const t3 = makeTicket({ id: 'T-C', title: 'C', dependsOn: ['T-A', 'T-B'] })
    const tree = makeTree({
      phases: [
        makePhase({
          epics: [
            makeEpic({
              waves: [makeWave({ sprints: [makeSprint({ tickets: [t1, t2, t3] })] })],
            }),
          ],
        }),
      ],
    })

    const edges = buildDependsOnEdges(tree)
    expect(edges).toHaveLength(2)
    const froms = edges.map((e) => e.fromId).sort()
    expect(froms).toEqual(['T-A', 'T-B'])
  })
})

// ── Optimistic mutation reducer ───────────────────────────────────────────────

describe('applyOptimisticStatusChange', () => {
  it('updates the target ticket status', () => {
    const tree = makeTree()
    const result = applyOptimisticStatusChange(tree, {
      ticketId: 'T-001',
      newStatus: 'active',
      previousStatus: 'planned',
    })
    const ticket = result.phases[0]!.epics[0]!.waves[0]!.sprints[0]!.tickets[0]!
    expect(ticket.status).toBe('active')
  })

  it('does NOT mutate the original tree', () => {
    const tree = makeTree()
    const originalStatus = tree.phases[0]!.epics[0]!.waves[0]!.sprints[0]!.tickets[0]!.status
    applyOptimisticStatusChange(tree, {
      ticketId: 'T-001',
      newStatus: 'done',
      previousStatus: 'planned',
    })
    expect(tree.phases[0]!.epics[0]!.waves[0]!.sprints[0]!.tickets[0]!.status).toBe(originalStatus)
  })

  it('leaves other tickets unchanged', () => {
    const t1 = makeTicket({ id: 'T-001', title: 'First' })
    const t2 = makeTicket({ id: 'T-002', title: 'Second', status: 'queue' })
    const tree = makeTree({
      phases: [
        makePhase({
          epics: [
            makeEpic({
              waves: [makeWave({ sprints: [makeSprint({ tickets: [t1, t2] })] })],
            }),
          ],
        }),
      ],
    })

    const result = applyOptimisticStatusChange(tree, {
      ticketId: 'T-001',
      newStatus: 'active',
      previousStatus: 'planned',
    })

    const tickets = result.phases[0]!.epics[0]!.waves[0]!.sprints[0]!.tickets
    expect(tickets[0]!.status).toBe('active')
    expect(tickets[1]!.status).toBe('queue') // unchanged
  })

  it('returns the original tree if ticketId is not found', () => {
    const tree = makeTree()
    const result = applyOptimisticStatusChange(tree, {
      ticketId: 'T-NONEXISTENT',
      newStatus: 'done',
      previousStatus: 'planned',
    })
    expect(result.phases[0]!.epics[0]!.waves[0]!.sprints[0]!.tickets[0]!.status).toBe('planned')
  })
})

describe('reconcileOnError', () => {
  it('reverses an optimistic change by restoring previousStatus', () => {
    // Start: ticket is planned; apply optimistic active; then reconcile on error
    const applied = makeTree({
      phases: [
        makePhase({
          epics: [
            makeEpic({
              waves: [
                makeWave({
                  sprints: [
                    makeSprint({ tickets: [makeTicket({ id: 'T-001', status: 'active' })] }),
                  ],
                }),
              ],
            }),
          ],
        }),
      ],
    })

    const reconciled = reconcileOnError(applied, {
      ticketId: 'T-001',
      newStatus: 'active',
      previousStatus: 'planned',
    })

    const ticket = reconciled.phases[0]!.epics[0]!.waves[0]!.sprints[0]!.tickets[0]!
    expect(ticket.status).toBe('planned')
  })

  it('does NOT mutate the input tree', () => {
    const tree = makeTree({
      phases: [
        makePhase({
          epics: [
            makeEpic({
              waves: [
                makeWave({
                  sprints: [makeSprint({ tickets: [makeTicket({ id: 'T-001', status: 'active' })] })],
                }),
              ],
            }),
          ],
        }),
      ],
    })
    reconcileOnError(tree, { ticketId: 'T-001', newStatus: 'active', previousStatus: 'planned' })
    expect(tree.phases[0]!.epics[0]!.waves[0]!.sprints[0]!.tickets[0]!.status).toBe('active')
  })
})

// ── Sync-event handler ────────────────────────────────────────────────────────

describe('mergeSyncEvent', () => {
  it('returns the incoming tree (disk is source of truth)', () => {
    const current = makeTree()
    const incoming = makeTree({
      phases: [makePhase({ id: 'P-99', title: 'New Phase' })],
      currentPhase: 'P-99',
    })
    const result = mergeSyncEvent(current, incoming)
    expect(result).toBe(incoming)
    expect(result.currentPhase).toBe('P-99')
  })

  it('does not use any data from the current (outgoing) tree', () => {
    const current = makeTree({ currentPhase: 'OLD' })
    const incoming = makeTree({ currentPhase: 'NEW' })
    const result = mergeSyncEvent(current, incoming)
    expect(result.currentPhase).toBe('NEW')
  })
})

// ── Flatten helpers ───────────────────────────────────────────────────────────

describe('flattenTickets', () => {
  it('returns all tickets as a flat array', () => {
    const t1 = makeTicket({ id: 'T-1' })
    const t2 = makeTicket({ id: 'T-2' })
    const t3 = makeTicket({ id: 'T-3' })
    const tree = makeTree({
      phases: [
        makePhase({
          id: 'P-01',
          epics: [
            makeEpic({
              waves: [
                makeWave({ sprints: [makeSprint({ tickets: [t1, t2] })] }),
                makeWave({ id: 'W-02', sprints: [makeSprint({ id: 'S-02', tickets: [t3] })] }),
              ],
            }),
          ],
        }),
      ],
    })

    const flat = flattenTickets(tree)
    expect(flat).toHaveLength(3)
    expect(flat.map((t) => t.id).sort()).toEqual(['T-1', 'T-2', 'T-3'])
  })

  it('returns empty array for a tree with no tickets', () => {
    const tree = makeTree({
      phases: [
        makePhase({
          epics: [makeEpic({ waves: [makeWave({ sprints: [makeSprint({ tickets: [] })] })] })],
        }),
      ],
    })
    expect(flattenTickets(tree)).toHaveLength(0)
  })
})

describe('ticketStatusCounts', () => {
  it('counts tickets grouped by status', () => {
    const tickets = [
      makeTicket({ id: 'T-1', status: 'active' }),
      makeTicket({ id: 'T-2', status: 'active' }),
      makeTicket({ id: 'T-3', status: 'done' }),
      makeTicket({ id: 'T-4', status: 'planned' }),
    ]
    const tree = makeTree({
      phases: [
        makePhase({
          epics: [makeEpic({ waves: [makeWave({ sprints: [makeSprint({ tickets })] })] })],
        }),
      ],
    })

    const counts = ticketStatusCounts(tree)
    expect(counts['active']).toBe(2)
    expect(counts['done']).toBe(1)
    expect(counts['planned']).toBe(1)
    expect(counts['blocked']).toBeUndefined()
  })
})

describe('findTicketById', () => {
  it('finds a ticket by id', () => {
    const target = makeTicket({ id: 'T-TARGET', title: 'Find me' })
    const tree = makeTree({
      phases: [
        makePhase({
          epics: [
            makeEpic({
              waves: [makeWave({ sprints: [makeSprint({ tickets: [makeTicket(), target] })] })],
            }),
          ],
        }),
      ],
    })

    const found = findTicketById(tree, 'T-TARGET')
    expect(found).not.toBeNull()
    expect(found!.title).toBe('Find me')
  })

  it('returns null if ticket is not found', () => {
    const tree = makeTree()
    expect(findTicketById(tree, 'T-MISSING')).toBeNull()
  })
})

describe('findPhaseById', () => {
  it('finds a phase by id', () => {
    const tree = makeTree()
    const found = findPhaseById(tree, 'P-01')
    expect(found).not.toBeNull()
    expect(found!.title).toBe('Phase 1')
  })

  it('returns null if phase is not found', () => {
    const tree = makeTree()
    expect(findPhaseById(tree, 'P-MISSING')).toBeNull()
  })
})

// ── Tree build from a multi-phase fixture ────────────────────────────────────

describe('PEWS tree from multi-phase fixture', () => {
  // A realistic fixture: 3 phases (past, current, future) with tickets
  function buildMultiPhaseTree(): PewsTree {
    const pastPhase = makePhase({
      id: 'P-1',
      title: 'Phase 1',
      status: 'shipped',
      epics: [
        makeEpic({
          id: 'E-P1-01',
          phaseId: 'P-1',
          waves: [
            makeWave({
              id: 'W-P1-01',
              epicId: 'E-P1-01',
              sprints: [
                makeSprint({
                  id: 'S-P1-01',
                  waveId: 'W-P1-01',
                  tickets: [
                    makeTicket({ id: 'T-P1-01', status: 'done', sprintId: 'S-P1-01' }),
                    makeTicket({ id: 'T-P1-02', status: 'archived', sprintId: 'S-P1-01' }),
                  ],
                }),
              ],
            }),
          ],
        }),
      ],
    })

    const currentPhase = makePhase({
      id: 'P-2',
      title: 'Phase 2',
      status: 'building',
      epics: [
        makeEpic({
          id: 'E-P2-01',
          phaseId: 'P-2',
          status: 'active',
          waves: [
            makeWave({
              id: 'W-P2-01',
              epicId: 'E-P2-01',
              status: 'active',
              sprints: [
                makeSprint({
                  id: 'S-P2-01',
                  waveId: 'W-P2-01',
                  status: 'active',
                  tickets: [
                    makeTicket({ id: 'T-P2-01', status: 'active', sprintId: 'S-P2-01' }),
                    makeTicket({
                      id: 'T-P2-02',
                      status: 'queue',
                      sprintId: 'S-P2-01',
                      dependsOn: ['T-P2-01'],
                    }),
                    makeTicket({ id: 'T-P2-03', status: 'blocked', sprintId: 'S-P2-01' }),
                  ],
                }),
              ],
            }),
          ],
        }),
      ],
    })

    const futurePhase = makePhase({
      id: 'P-3',
      title: 'Phase 3',
      status: 'planning',
      epics: [
        makeEpic({
          id: 'E-P3-01',
          phaseId: 'P-3',
          waves: [
            makeWave({
              id: 'W-P3-01',
              epicId: 'E-P3-01',
              sprints: [
                makeSprint({
                  id: 'S-P3-01',
                  waveId: 'W-P3-01',
                  tickets: [
                    makeTicket({ id: 'T-P3-01', status: 'planned', sprintId: 'S-P3-01' }),
                  ],
                }),
              ],
            }),
          ],
        }),
      ],
    })

    return {
      phases: [pastPhase, currentPhase, futurePhase],
      currentPhase: 'P-2',
      currentEpic: 'E-P2-01',
      currentWave: 'W-P2-01',
      currentSprint: 'S-P2-01',
      activeTickets: ['T-P2-01'],
    }
  }

  it('classifies phases correctly across past/current/future', () => {
    const tree = buildMultiPhaseTree()
    const temporal = classifyAllPhases(tree)
    expect(temporal.get('P-1')).toBe('past')
    expect(temporal.get('P-2')).toBe('current')
    expect(temporal.get('P-3')).toBe('future')
  })

  it('flattens all 6 tickets from 3 phases', () => {
    const tree = buildMultiPhaseTree()
    const flat = flattenTickets(tree)
    expect(flat).toHaveLength(6)
  })

  it('extracts the T-P2-01 → T-P2-02 depends_on edge', () => {
    const tree = buildMultiPhaseTree()
    const edges = buildDependsOnEdges(tree)
    expect(edges).toHaveLength(1)
    expect(edges[0]).toMatchObject({
      fromId: 'T-P2-01',
      toId: 'T-P2-02',
    })
  })

  it('counts status distribution across all phases', () => {
    const tree = buildMultiPhaseTree()
    const counts = ticketStatusCounts(tree)
    expect(counts['done']).toBe(1)
    expect(counts['archived']).toBe(1)
    expect(counts['active']).toBe(1)
    expect(counts['queue']).toBe(1)
    expect(counts['blocked']).toBe(1)
    expect(counts['planned']).toBe(1)
  })

  it('optimistically updates a ticket and can reconcile on error', () => {
    const tree = buildMultiPhaseTree()

    // Optimistic: mark T-P2-03 (blocked) as active
    const optimistic = applyOptimisticStatusChange(tree, {
      ticketId: 'T-P2-03',
      newStatus: 'active',
      previousStatus: 'blocked',
    })
    expect(findTicketById(optimistic, 'T-P2-03')!.status).toBe('active')

    // Error: reconcile back to blocked
    const reconciled = reconcileOnError(optimistic, {
      ticketId: 'T-P2-03',
      newStatus: 'active',
      previousStatus: 'blocked',
    })
    expect(findTicketById(reconciled, 'T-P2-03')!.status).toBe('blocked')
  })

  it('live sync replaces the tree with the incoming version', () => {
    const tree = buildMultiPhaseTree()
    const incoming = makeTree({ currentPhase: 'P-3' })
    const merged = mergeSyncEvent(tree, incoming)
    expect(merged.currentPhase).toBe('P-3')
    expect(merged.phases).toHaveLength(1) // incoming has only 1 phase
  })
})
