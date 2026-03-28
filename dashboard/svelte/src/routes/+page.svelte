<script lang="ts">
  import { onMount } from 'svelte';
  import type { ChartDataset } from 'chart.js';
  import { fetchJSON } from '$lib/api';
  import { formatUSD } from '$lib/currency';
  import { connectStream } from '$lib/sse';
  import { AlertItem, KPICard, LineChart } from '$lib/components';
  import type {
    Agent,
    AgentsResponse,
    Alert,
    AlertsResponse,
    CostBreakdown,
    CostsResponse,
    HealthResponse
  } from '$lib/types';

  type HourlyCostBreakdown = CostBreakdown & {
    hour?: string;
    timestamp?: string;
    bucket?: string;
    period?: string;
    time?: string;
  };

  let loading = $state(true);
  let errorMessage = $state<string | null>(null);
  let totalSpendToday = $state(0);
  let activeAgents = $state(0);
  let alertsToday = $state(0);
  let uptimeSeconds = $state(0);
  let labels = $state<string[]>([]);
  let values = $state<number[]>([]);
  let recentAlerts = $state<Alert[]>([]);

  const lineDatasets = $derived<ChartDataset<'line', number[]>[]>([
    {
      label: 'Cost (USD)',
      data: values,
      borderColor: '#3B82F6',
      backgroundColor: '#3B82F6'
    }
  ]);

  function formatUptime(seconds: number): string {
    if (seconds < 60) return `${seconds}s`;
    const days = Math.floor(seconds / 86400);
    const hours = Math.floor((seconds % 86400) / 3600);
    const mins = Math.floor((seconds % 3600) / 60);
    if (days > 0) return `${days}d ${hours}h`;
    if (hours > 0) return `${hours}h ${mins}m`;
    return `${mins}m`;
  }

  function toHourLabel(item: HourlyCostBreakdown, index: number): string {
    const candidate =
      item.hour ?? item.timestamp ?? item.bucket ?? item.period ?? item.time ?? `hour-${index + 1}`;
    const parsed = new Date(candidate);
    if (Number.isNaN(parsed.getTime())) {
      return `H${index + 1}`;
    }
    return parsed.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  }

  async function loadOverview(): Promise<void> {
    loading = true;
    errorMessage = null;

    try {
      const from = new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString();
      const [costs, agentsRes, health, alertsRes] = await Promise.all([
        fetchJSON<CostsResponse>(`/costs?group_by=hour&from=${encodeURIComponent(from)}`),
        fetchJSON<AgentsResponse>('/agents'),
        fetchJSON<HealthResponse>('/health'),
        fetchJSON<AlertsResponse>(`/alerts?from=${encodeURIComponent(from)}`)
      ]);

      const hourly = costs.breakdown as HourlyCostBreakdown[];
      labels = hourly.map((point, index) => toHourLabel(point, index));
      values = hourly.map((point) => point.cost_usd);

      totalSpendToday = costs.total_usd;
      activeAgents = agentsRes.agents.filter((agent: Agent) => agent.status === 'active').length;
      alertsToday = alertsRes.alerts.length;
      uptimeSeconds = health.uptime_seconds;
      recentAlerts = alertsRes.alerts.slice(0, 5);
    } catch (err) {
      errorMessage = err instanceof Error ? err.message : 'Failed to load overview data.';
    } finally {
      loading = false;
    }
  }

  async function emergencyStop(): Promise<void> {
    if (!confirm('Emergency stop will disable all agents. Continue?')) {
      return;
    }

    try {
      await fetchJSON('/kill-all', { method: 'POST' });
      await loadOverview();
    } catch (err) {
      errorMessage = err instanceof Error ? err.message : 'Emergency stop failed.';
    }
  }

  onMount(() => {
    void loadOverview();

    const stream = connectStream((eventName) => {
      if (eventName === 'cost_update' || eventName === 'budget_alert' || eventName === 'agent_killed') {
        void loadOverview();
      }
    });

    return () => {
      stream.close();
    };
  });
</script>

<section class="space-y-6">
  <header class="space-y-1">
    <h1 class="text-2xl font-semibold text-text-primary">Overview</h1>
    <p class="text-sm text-text-secondary">Live spend, alerts, and system health.</p>
  </header>

  {#if errorMessage}
    <div class="rounded-lg border border-danger/40 bg-danger/10 p-4">
      <p class="text-sm text-danger">{errorMessage}</p>
      <button
        type="button"
        class="mt-3 rounded-md bg-accent px-3 py-1.5 text-xs font-medium text-white hover:bg-accent-hover"
        onclick={() => loadOverview()}
      >
        Retry
      </button>
    </div>
  {/if}

  <div class="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-4">
    <KPICard title="Total Spend Today" value={formatUSD(totalSpendToday)} subtitle="Last 24 hours" />
    <KPICard title="Active Agents" value={activeAgents} subtitle="Currently serving traffic" />
    <div class={alertsToday > 0 ? 'rounded-lg ring-1 ring-warning/60' : ''}>
      <KPICard
        title="Alerts Today"
        value={alertsToday}
        subtitle="Recent alert events"
        trend={alertsToday > 0 ? 'down' : 'up'}
        trendLabel={alertsToday > 0 ? 'Needs attention' : 'All clear'}
      />
    </div>
    <KPICard title="Uptime" value={formatUptime(uptimeSeconds)} subtitle="Proxy process uptime" />
  </div>

  {#if loading}
    <section class="flex h-[320px] items-center justify-center rounded-lg border border-border-default bg-surface p-4 text-sm text-text-secondary">
      Loading overview data...
    </section>
  {:else if values.length === 0}
    <section class="flex h-[320px] items-center justify-center rounded-lg border border-border-default bg-surface p-4 text-center text-sm text-text-secondary">
      No cost data yet. Proxy some requests to see cost trends.
    </section>
  {:else}
    <LineChart {labels} datasets={lineDatasets} height={320} />
  {/if}

  <section class="space-y-3 rounded-lg border border-border-default bg-surface p-4">
    <h2 class="text-lg font-semibold text-text-primary">Recent Alerts</h2>
    {#if recentAlerts.length === 0}
      <div class="flex items-center gap-2 text-sm text-text-secondary">
        <span class="h-2.5 w-2.5 rounded-full bg-success" aria-hidden="true"></span>
        <span>No alerts. Everything is running smoothly.</span>
      </div>
    {:else}
      <div class="space-y-2">
        {#each recentAlerts as alert (alert.id)}
          <AlertItem {alert} />
        {/each}
      </div>
    {/if}
  </section>

  <button
    type="button"
    class="w-full rounded-md bg-danger px-4 py-3 text-sm font-semibold text-white transition-colors hover:bg-red-600"
    onclick={emergencyStop}
  >
    Emergency Stop
  </button>
</section>
