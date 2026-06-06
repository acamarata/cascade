// @vitest-environment jsdom
/**
 * Purpose: WCAG 2.1 AA accessibility regression test for the app-shell navigation.
 *   Asserts the Sidebar landmark + nav items have no axe-detectable violations and
 *   that the skip-to-content link is present and points at the main landmark.
 * Constraints: Sidebar uses NavItem (react-router NavLink) so it must render inside a
 *   Router. jest-axe runs the axe-core ruleset against the rendered DOM.
 * SPORT: MASTER-COMPONENTS.md — Sidebar, SkipToMain (E01-15 WCAG remediation)
 */

import { render } from '@testing-library/react'
import '@testing-library/jest-dom'
import { axe, toHaveNoViolations } from 'jest-axe'
import { MemoryRouter } from 'react-router-dom'
import { Sidebar } from './Sidebar'
import { SkipToMain } from '../SkipToMain'

expect.extend(toHaveNoViolations)

test('Sidebar nav landmark has no axe violations', async () => {
  const { container } = render(
    <MemoryRouter>
      <Sidebar />
    </MemoryRouter>
  )
  const results = await axe(container)
  expect(results).toHaveNoViolations()
})

test('Sidebar exposes a labelled navigation landmark', () => {
  const { getByRole } = render(
    <MemoryRouter>
      <Sidebar />
    </MemoryRouter>
  )
  expect(getByRole('navigation', { name: /main navigation/i })).toBeInTheDocument()
})

test('SkipToMain links to the main-content landmark', () => {
  const { getByText } = render(<SkipToMain />)
  const link = getByText(/skip to main content/i)
  expect(link).toHaveAttribute('href', '#main-content')
})
