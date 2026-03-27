<script lang="ts">
  import { onMount } from 'svelte';
  import type { ChartDataset } from 'chart.js';
  import { fetchBlob, fetchJSON } from '$lib/api';
  import { BarChart, DataTable, DateRangePicker, KPICard, LineChart } from '$lib/components';
  import type { CostBreakdown, CostsResponse } from '$lib/types';
  import type { Snippet } from 'svelte';

  type RowData = Record<string, string | number | boolean | null | undefined>;
  type ColumnDef = {
    key: string;
    label: string;
    sortable?: boolean;
  };
  type DateRangePreset = 'today' | '7d' | '30d' | 'custom';

  type CostRow = CostBreakdown & RowData;

  const columns: ColumnDef[] = [
    { key: 'agent', label: 'Agent', sortable: true },
    { key: 'model', label: 'Model', sortable: true },
    { key: 'requests', label: 'Requests', sortable: true },
    { key: 'input_tokens', label: 'Input Tokens', sortable: true },
    { key: 'output_tokens', label: 'Output Tokens', sortable: true },
    { key: 'cost_usd', label: 'Cost (USD)', sortable: true }
  ];

  let selectedRange = $state<DateRangePreset>('today');
  let loading = $state(true);
  let errorMessage = $state<string | null>(null);
  let totalCostUSD = $state(0);
  let rows = $state<CostRow[]>([]);

  const barByAgent = $derived.by(() => {
    const totals = new Map<string, number>();
    for (const row of rows) {
      const key = row.agent || 'unknown';
      totals.set(key, (totals.get(key) ?? 0) + row.cost_usd);
    }
    const entries = [...totals.entries()].sort((a, b) => b[1] - a[1]);
    return {
      labels: entries.map(([label]) => label),
      values: entries.map(([, value]) => value)
    };
  });

  const barByModel = $derived.by(() => {
    const totals = new Map<string, number>();
    for (const row of rows) {
      const key = row.model || 'unknown';
      totals.set(key, (totals.get(key) ?? 0) + row.cost_usd);
    }
    const entries = [...totals.entries()].sort((a, b) => b[1] - a[1]);
    return {
      labels: entries.map(([label]) => label),
      values: entries.map(([, value]) => value)
    };
  });

  const lineData = $derived.by(() => {
    const buckets = new Set<string>();
    const byAgent = new Map<string, Map<string, number>>();

    for (const row of rows) {
      const bucket = toHourBucket(row.bucket);
      buckets.add(bucket);
      const agent = row.agent || 'unknown';
      if (!byAgent.has(agent)) {
        byAgent.set(agent, new Map<string, number>());
      }
      const agentBuckets = byAgent.get(agent) as Map<string, number>;
      agentBuckets.set(bucket, (agentBuckets.get(bucket) ?? 0) + row.cost_usd);
    }

    const sortedBuckets = [...buckets].sort((a, b) => new Date(a).getTime() - new Date(b).getTime());
    const labels = sortedBuckets.map((bucket) =>
      new Date(bucket).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
    );

    const datasets: ChartDataset<'line', number[]>[] = [...byAgent.entries()].map(
      ([agent, values]) => ({
        label: agent,
        data: sortedBuckets.map((bucket) => values.get(bucket) ?? 0),
        fill: true
      })
    );

    return { labels, datasets };
  });

  const cellRenderers = $derived.by<Record<string, Snippet<[RowData]>>>(() => ({
    cost_usd: costCell
  }));

  function toHourBucket(raw?: string): string {
    if (!raw) {
      return new Date(0).toISOString();
    }
    const parsed = new Date(raw);
    if (Number.isNaN(parsed.getTime())) {
      return new Date(0).toISOString();
    }
    parsed.setMinutes(0, 0, 0);
    return parsed.toISOString();
  }

  function formatUSD(value: number): string {
    return new Intl.NumberFormat('en-US', {
      style: 'currency',
      currency: 'USD',
      maximumFractionDigits: 2
    }).format(value);
  }

  function fromForRange(range: DateRangePreset): string {
    const now = new Date();
    if (range === '7d') {
      return new Date(now.getTime() - 7 * 24 * 60 * 60 * 1000).toISOString();
    }
    if (range === '30d') {
      return new Date(now.getTime() - 30 * 24 * 60 * 60 * 1000).toISOString();
    }
    return new Date(now.getTime() - 24 * 60 * 60 * 1000).toISOString();
  }

  function queryForRange(range: DateRangePreset): string {
    const from = fromForRange(range);
    return `from=${encodeURIComponent(from)}&to=${encodeURIComponent(new Date().toISOString())}`;
  }

  async function loadCosts(range: DateRangePreset): Promise<void> {
    loading = true;
    errorMessage = null;

    try {
      const query = queryForRange(range);
      const response = await fetchJSON<CostsResponse>(`/costs?group_by=none&${query}`);
      totalCostUSD = response.total_usd;
      rows = response.breakdown as CostRow[];
    } catch (err) {
      errorMessage = err instanceof Error ? err.message : 'Failed to load costs.';
      rows = [];
      totalCostUSD = 0;
    } finally {
      loading = false;
    }
  }

  async function changeRange(next: DateRangePreset): Promise<void> {
    selectedRange = next;
    await loadCosts(next);
  }

  async function exportCSV(): Promise<void> {
    try {
      const query = queryForRange(selectedRange);
      const csv = await fetchBlob(`/costs/export?group_by=none&${query}`);
      const url = URL.createObjectURL(csv);
      const link = document.createElement('a');
      link.href = url;
      link.download = `oberwatch-costs-${selectedRange}.csv`;
      link.click();
      URL.revokeObjectURL(url);
    } catch (err) {
      errorMessage = err instanceof Error ? err.message : 'Failed to export CSV.';
    }
  }

  onMount(() => {
    void loadCosts('today');
  });
</script>

{#snippet costCell(raw: RowData)}
  {@const row = raw as CostRow}
  <span class="font-mono text-[13px]">{formatUSD(row.cost_usd)}</span>
{/snippet}

<section class="space-y-4">
  <header class="space-y-1">
    <h1 class="text-2xl font-semibold text-text-primary">Costs</h1>
    <p class="text-sm text-text-secondary">Cost attribution and trend analysis across agents and models.</p>
  </header>

  <div class="flex flex-col gap-3 rounded-lg border border-border-default bg-surface p-3 md:flex-row md:items-center md:justify-between">
    <DateRangePicker selected={selectedRange} onChange={changeRange} />
    <button
      type="button"
      class="rounded-md border border-border-default bg-elevated px-3 py-1.5 text-xs font-medium text-text-primary hover:bg-accent hover:text-white"
      onclick={exportCSV}
    >
      Export CSV
    </button>
  </div>

  {#if errorMessage}
    <div class="rounded-lg border border-danger/40 bg-danger/10 p-4">
      <p class="text-sm text-danger">{errorMessage}</p>
      <button
        type="button"
        class="mt-3 rounded-md bg-accent px-3 py-1.5 text-xs font-medium text-white hover:bg-accent-hover"
        onclick={() => loadCosts(selectedRange)}
      >
        Retry
      </button>
    </div>
  {/if}

  <KPICard title="Total Cost" value={formatUSD(totalCostUSD)} subtitle={`Range: ${selectedRange}`} />

  {#if loading}
    <div class="grid grid-cols-1 gap-4 xl:grid-cols-2">
      <div class="h-72 animate-pulse rounded-lg border border-border-default bg-surface"></div>
      <div class="h-72 animate-pulse rounded-lg border border-border-default bg-surface"></div>
    </div>
  {:else if rows.length === 0}
    <div class="rounded-lg border border-border-default bg-surface p-8 text-center text-sm text-text-muted">
      No cost data available for this range.
    </div>
  {:else}
    <div class="grid grid-cols-1 gap-4 xl:grid-cols-2">
      <BarChart labels={barByAgent.labels} values={barByAgent.values} height={320} />
      <BarChart labels={barByModel.labels} values={barByModel.values} height={320} />
    </div>

    <LineChart labels={lineData.labels} datasets={lineData.datasets} height={340} />

    <DataTable {columns} rows={rows} {cellRenderers} />
  {/if}
</section>
