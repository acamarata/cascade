/**
 * Purpose: Root layout shell — sidebar + header + scrollable main content area.
 * Inputs: none — renders child routes via React Router <Outlet />.
 * Outputs: Full-screen flex layout with sidebar, header, and main content.
 * Constraints: h-screen + overflow-hidden on shell; overflow-y-auto on main for scrollable content.
 * SPORT: T-P3-E02-04 layout shell
 */
import { Outlet } from 'react-router-dom'
import { Sidebar } from '@/components/Sidebar'
import { Header } from '@/components/Header'
import { GPChatButton } from '@/components/GPChatPanel/GPChatButton'

export function DashboardLayout() {
  return (
    <div className="flex h-screen overflow-hidden bg-background text-foreground">
      <Sidebar />
      <div className="flex flex-col flex-1 overflow-hidden">
        <Header />
        <main
          className="flex-1 overflow-y-auto p-6"
          id="main-content"
          tabIndex={-1}
        >
          <Outlet />
        </main>
        <GPChatButton />
      </div>
    </div>
  )
}
