<script lang="ts">
  import { onMount } from 'svelte';
  import type { ChartDataset } from 'chart.js';
  import { fetchJSON } from '$lib/api';
  import { formatUSD } from '$lib/currency';
  import { connectStream } from '$lib/sse';
  import { AlertItem, ErrorState, KPICard, LineChart, SkeletonChart, SkeletonKPICard } from '$lib/components';
  import { loadPhase } from '$lib/loadState';
  import type {
    Agent,
    AgentsResponse,
    Alert,
    AlertsResponse,
    BudgetsResponse,
    CostBreakdown,
    CostsResponse,
    GlobalBudget,
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
  let emergencyStopActive = $state(false);
  let emergencyBusy = $state(false);
  let labels = $state<string[]>([]);
  let values = $state<number[]>([]);
  let recentAlerts = $state<Alert[]>([]);
  let globalBudget = $state<GlobalBudget | null>(null);

  const phase = $derived(loadPhase({ loading, errorMessage, hasData: values.length > 0 }));

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
      const [costs, agentsRes, health, alertsRes, budgetsRes] = await Promise.all([
        fetchJSON<CostsResponse>(`/costs?group_by=hour&from=${encodeURIComponent(from)}`),
        fetchJSON<AgentsResponse>('/agents'),
        fetchJSON<HealthResponse>('/health'),
        fetchJSON<AlertsResponse>(`/alerts?from=${encodeURIComponent(from)}`),
        fetchJSON<BudgetsResponse>('/budgets')
      ]);

      const hourly = costs.breakdown as HourlyCostBreakdown[];
      labels = hourly.map((point, index) => toHourLabel(point, index));
      values = hourly.map((point) => point.cost_usd);

      totalSpendToday = costs.total_usd;
      activeAgents = agentsRes.agents.filter((agent: Agent) => agent.status === 'active').length;
      alertsToday = alertsRes.alerts.length;
      uptimeSeconds = health.uptime_seconds;
      emergencyStopActive = health.emergency_stop ?? false;
      recentAlerts = alertsRes.alerts.slice(0, 5);
      globalBudget = budgetsRes.global.limit_usd > 0 ? budgetsRes.global : null;
    } catch (err) {
      errorMessage = err instanceof Error ? err.message : 'Failed to load overview data.';
    } finally {
      loading = false;
    }
  }

  async function emergencyStop(): Promise<void> {
    if (
      !confirm(
        'This will pause ALL agent requests immediately. The dashboard and API will remain accessible. Are you sure?'
      )
    ) {
      return;
    }

    try {
      await fetchJSON('/kill-all', { method: 'POST' });
      await loadOverview();
    } catch (err) {
      errorMessage = err instanceof Error ? err.message : 'Emergency stop failed.';
    }
  }

  async function resumeOperations(): Promise<void> {
    emergencyBusy = true;
    try {
      await fetchJSON('/resume', { method: 'POST' });
      await loadOverview();
    } catch (err) {
      errorMessage = err instanceof Error ? err.message : 'Resume failed.';
    } finally {
      emergencyBusy = false;
    }
  }

  onMount(() => {
    void loadOverview();

    const stream = connectStream((eventName) => {
      if (
        eventName === 'cost_update' ||
        eventName === 'budget_alert' ||
        eventName === 'agent_killed' ||
        eventName === 'emergency_stop'
      ) {
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

  {#if emergencyStopActive}
    <div class="flex items-center justify-between gap-4 rounded-lg border border-warning/40 bg-warning/10 px-4 py-3">
      <p class="text-sm font-medium text-warning">Emergency Stop Active — All agent requests are paused.</p>
      <button
        type="button"
        class="rounded-md bg-success px-4 py-2 text-sm font-medium text-white disabled:opacity-60"
        disabled={emergencyBusy}
        onclick={resumeOperations}
      >
        Resume Operations
      </button>
    </div>
  {/if}

  {#if phase === 'error'}
    <ErrorState message={errorMessage ?? 'Failed to load overview data.'} onRetry={loadOverview} />
  {/if}

  <div class="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-4">
    {#if phase === 'loading'}
      <SkeletonKPICard label="Loading total spend today" />
      <SkeletonKPICard label="Loading active agents" />
      <SkeletonKPICard label="Loading alerts today" />
      <SkeletonKPICard label="Loading uptime" />
    {:else if phase !== 'error'}
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
    {/if}
  </div>

  {#if globalBudget}
    <div class="rounded-lg border border-border-default bg-surface p-4">
      <div class="flex items-center justify-between gap-4">
        <div>
          <p class="text-xs font-medium uppercase tracking-wider text-text-secondary">Global Budget</p>
          <p class="mt-1 text-2xl font-semibold text-text-primary">
            {formatUSD(globalBudget.spent_usd)} <span class="text-sm font-normal text-text-secondary">/ {formatUSD(globalBudget.limit_usd)}</span>
          </p>
          <p class="mt-0.5 text-xs text-text-secondary capitalize">{globalBudget.period} · {globalBudget.percentage_used.toFixed(1)}% used</p>
        </div>
        <div class="h-2 w-32 overflow-hidden rounded-full bg-bg-elevated">
          <div
            class={`h-full rounded-full transition-all ${globalBudget.percentage_used >= 90 ? 'bg-danger' : globalBudget.percentage_used >= 75 ? 'bg-warning' : 'bg-accent'}`}
            style={`width: ${Math.min(globalBudget.percentage_used, 100)}%`}
          ></div>
        </div>
      </div>
    </div>
  {/if}

  {#if phase === 'loading'}
    <SkeletonChart height={320} label="Loading cost trend" />
  {:else if phase === 'empty'}
    <section class="flex h-[320px] items-center justify-center rounded-lg border border-border-default bg-surface p-4 text-center text-sm text-text-secondary">
      No cost data yet. Proxy some requests to see cost trends.
    </section>
  {:else if phase === 'ready'}
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

  {#if !emergencyStopActive}
    <button
      type="button"
      class="w-full rounded-md bg-danger px-4 py-3 text-sm font-semibold text-white transition-colors hover:bg-red-600"
      onclick={emergencyStop}
    >
      Emergency Stop
    </button>
  {/if}
</section>
