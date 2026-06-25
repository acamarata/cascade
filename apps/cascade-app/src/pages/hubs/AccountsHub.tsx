/**
 * Purpose: Hub page for the Accounts section, grouping Fleet, Providers, and Usage
 *          into a tabbed layout driven by the `?tab=` search param.
 * Inputs:  URLSearchParams `tab` — one of 'fleet' | 'providers' | 'usage'
 * Outputs: Renders the matching sub-page inside TabsLayout
 * Constraints: Default tab is 'fleet'. Unknown tab values fall back to 'fleet'.
 * SPORT: accounts-hub
 */

import { useSearchParams } from 'react-router-dom';
import { Users, Plug, BarChart2 } from 'lucide-react';
import { TabsLayout } from '../../components/layout/TabsLayout';
import { AccountsPage } from '../AccountsPage';
import { ProviderSettings } from '../ProviderSettings';
import { UsagePage } from '../UsagePage';

type TabId = 'fleet' | 'providers' | 'usage';

const TABS = [
  { id: 'fleet' as TabId, label: 'Fleet', icon: Users },
  { id: 'providers' as TabId, label: 'Providers', icon: Plug },
  { id: 'usage' as TabId, label: 'Usage', icon: BarChart2 },
];

const VALID_TABS = new Set<string>(TABS.map((t) => t.id));

function resolveTab(raw: string | null): TabId {
  if (raw && VALID_TABS.has(raw)) return raw as TabId;
  return 'fleet';
}

export default function AccountsHub() {
  const [searchParams, setSearchParams] = useSearchParams();
  const activeTab = resolveTab(searchParams.get('tab'));

  function handleTabChange(id: string) {
    setSearchParams({ tab: id }, { replace: true });
  }

  return (
    <div className="h-full flex flex-col">
      <TabsLayout
        tabs={TABS}
        activeTab={activeTab}
        onTabChange={handleTabChange}
      >
        {activeTab === 'fleet' && <AccountsPage />}
        {activeTab === 'providers' && <ProviderSettings />}
        {activeTab === 'usage' && <UsagePage />}
      </TabsLayout>
    </div>
  );
}
