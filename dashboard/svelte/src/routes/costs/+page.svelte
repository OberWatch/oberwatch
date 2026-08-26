<script lang="ts">
  import { onMount } from 'svelte';
  import type { ChartDataset } from 'chart.js';
  import { fetchBlob, fetchJSON } from '$lib/api';
  import { formatUSD } from '$lib/currency';
  import {
    BarChart,
    DataTable,
    DateRangePicker,
    ErrorState,
    KPICard,
    LineChart,
    SkeletonChart,
    SkeletonKPICard
  } from '$lib/components';
  import { buildStackedSeries, describeStackedSeries, totalsByAgent, totalsByModel } from '$lib/costs';
  import { createCostsLoader } from '$lib/costsLoader';
  import { loadPhase } from '$lib/loadState';
  import {
    canExportSelection,
    costsExportQuery,
    type DateRangeSelection,
    type ResolvedRange
  } from '$lib/dateRange';
  import type { CostBreakdown, CostsResponse } from '$lib/types';
  import type { Snippet } from 'svelte';

  type RowData = Record<string, string | number | boolean | null | undefined>;
  type ColumnDef = {
    key: string;
    label: string;
    sortable?: boolean;
  };

  type CostRow = CostBreakdown & RowData;

  const columns: ColumnDef[] = [
    { key: 'agent', label: 'Agent', sortable: true },
    { key: 'model', label: 'Model', sortable: true },
    { key: 'requests', label: 'Requests', sortable: true },
    { key: 'input_tokens', label: 'Input Tokens', sortable: true },
    { key: 'output_tokens', label: 'Output Tokens', sortable: true },
    { key: 'cost_usd', label: 'Cost (USD)', sortable: true }
  ];

  let selection = $state<DateRangeSelection>({ preset: 'today', customStart: '', customEnd: '' });
  let activeRange = $state<ResolvedRange | null>(null);
  // The selection that produced activeRange. It lags `selection` while a load is
  // in flight, and stays behind entirely when a custom range fails validation.
  let activeSelection = $state<DateRangeSelection | null>(null);
  let rangeError = $state<string | null>(null);
  let loading = $state(true);
  let errorMessage = $state<string | null>(null);
  let totalCostUSD = $state(0);
  let rows = $state<CostRow[]>([]);
  let seriesRows = $state<CostBreakdown[]>([]);

  const phase = $derived(loadPhase({ loading, errorMessage, hasData: rows.length > 0 }));

  const barByAgent = $derived(totalsByAgent(rows));
  const barByModel = $derived(totalsByModel(rows));

  const series = $derived(
    buildStackedSeries(seriesRows, activeRange?.bucket ?? 'hour')
  );

  const lineDatasets = $derived<ChartDataset<'line', number[]>[]>(
    series.datasets.map((dataset) => ({ label: dataset.label, data: dataset.data }))
  );

  const rangeLabel = $derived(activeRange?.label ?? 'Today');
  const seriesDescription = $derived(describeStackedSeries(series, rangeLabel));

  // Only offer the export while it would describe what is on screen.
  const exportEnabled = $derived(
    activeRange !== null && canExportSelection(selection, activeSelection)
  );

  const cellRenderers = $derived.by<Record<string, Snippet<[RowData]>>>(() => ({
    cost_usd: costCell
  }));

  const loader = createCostsLoader(
    (query) => fetchJSON<CostsResponse>(`/costs?${query}`),
    () => new Date()
  );

  /**
   * loadCosts resolves the selection once and then fetches the totals and the
   * time series for that single window, so the KPI, both bar charts, the stacked
   * series, the table and the CSV export all describe the same instants.
   *
   * A load the user has already moved on from comes back as `stale` and is
   * dropped, so a slow response cannot overwrite a newer range.
   */
  async function loadCosts(next: DateRangeSelection): Promise<void> {
    loading = true;
    const outcome = await loader.load(next);

    if (outcome.status === 'stale') {
      // A newer load owns the page state, including the loading flag.
      return;
    }

    loading = false;

    if (outcome.status === 'invalid') {
      rangeError = outcome.error;
      // No request went out for this selection, so a prior API failure is now
      // stale. Clear it so the fatal ErrorState banner does not sit alongside
      // the range picker's own validation message.
      errorMessage = null;
      return;
    }

    rangeError = null;
    activeRange = outcome.range;
    activeSelection = next;

    if (outcome.status === 'failed') {
      errorMessage = outcome.error;
      rows = [];
      seriesRows = [];
      totalCostUSD = 0;
      return;
    }

    errorMessage = null;
    totalCostUSD = outcome.totalUSD;
    rows = outcome.rows as CostRow[];
    seriesRows = outcome.seriesRows;
  }

  function changeSelection(next: DateRangeSelection): void {
    selection = next;
    void loadCosts(next);
  }

  /** draftSelection records typing in the custom inputs without querying. */
  function draftSelection(next: DateRangeSelection): void {
    selection = next;
  }

  function retry(): void {
    void loadCosts(selection);
  }

  async function exportCSV(): Promise<void> {
    if (!exportEnabled || !activeRange) {
      return;
    }

    try {
      const csv = await fetchBlob(`/costs/export?${costsExportQuery(activeRange)}`);
      const url = URL.createObjectURL(csv);
      const link = document.createElement('a');
      link.href = url;
      link.download = `oberwatch-costs-${activeRange.preset}.csv`;
      link.click();
      URL.revokeObjectURL(url);
    } catch (err) {
      errorMessage = err instanceof Error ? err.message : 'Failed to export CSV.';
    }
  }

  onMount(() => {
    void loadCosts(selection);
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

  <div class="flex flex-col gap-3 rounded-lg border border-border-default bg-surface p-3 md:flex-row md:items-start md:justify-between">
    <DateRangePicker
      {selection}
      error={rangeError}
      onChange={changeSelection}
      onDraft={draftSelection}
    />
    <button
      type="button"
      disabled={!exportEnabled}
      title={exportEnabled ? undefined : 'Apply a range to export it'}
      class="rounded-md border border-border-default bg-elevated px-3 py-1.5 text-xs font-medium text-text-primary hover:bg-accent hover:text-white disabled:cursor-not-allowed disabled:opacity-50"
      onclick={exportCSV}
    >
      Export CSV
    </button>
  </div>

  {#if phase === 'error'}
    <ErrorState message={errorMessage ?? 'Failed to load costs.'} onRetry={retry} />
  {/if}

  {#if phase === 'loading'}
    <SkeletonKPICard label="Loading total cost" />
  {:else if phase !== 'error'}
    <KPICard title="Total Cost" value={formatUSD(totalCostUSD)} subtitle={`Range: ${rangeLabel}`} />
  {/if}

  {#if phase === 'loading'}
    <div class="grid grid-cols-1 gap-4 xl:grid-cols-2">
      <SkeletonChart height={320} label="Loading cost by agent" />
      <SkeletonChart height={320} label="Loading cost by model" />
    </div>
  {:else if phase === 'empty'}
    <div class="rounded-lg border border-border-default bg-surface p-8 text-center text-sm text-text-muted">
      No cost data available for {rangeLabel}.
    </div>
  {:else if phase === 'ready'}
    <div class="grid grid-cols-1 gap-4 xl:grid-cols-2">
      <BarChart labels={barByAgent.labels} values={barByAgent.values} height={320} />
      <BarChart labels={barByModel.labels} values={barByModel.values} height={320} />
    </div>

    {#if series.datasets.length > 0}
      <LineChart
        stacked
        ariaLabel={seriesDescription}
        labels={series.labels}
        datasets={lineDatasets}
        height={340}
      />

      <details class="rounded-lg border border-border-default bg-surface p-3">
        <summary class="cursor-pointer text-xs font-medium text-text-secondary">
          Show cost over time as a table
        </summary>
        <p class="mt-2 text-xs text-text-muted">{seriesDescription}</p>
        <div class="mt-2 overflow-x-auto">
          <table class="w-full text-left text-xs">
            <caption class="sr-only">Cost in USD per agent for each time bucket in {rangeLabel}</caption>
            <thead>
              <tr class="text-text-muted">
                <th scope="col" class="px-2 py-1 font-medium">Time</th>
                {#each series.datasets as dataset (dataset.label)}
                  <th scope="col" class="px-2 py-1 font-medium">{dataset.label}</th>
                {/each}
              </tr>
            </thead>
            <tbody>
              {#each series.labels as bucketLabel, bucketIndex (series.bucketKeys[bucketIndex])}
                <tr class="border-t border-border-default">
                  <th scope="row" class="px-2 py-1 font-normal text-text-secondary">{bucketLabel}</th>
                  {#each series.datasets as dataset (dataset.label)}
                    <td class="px-2 py-1 font-mono text-text-primary">
                      {formatUSD(dataset.data[bucketIndex])}
                    </td>
                  {/each}
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      </details>
    {:else}
      <div class="rounded-lg border border-border-default bg-surface p-8 text-center text-sm text-text-muted">
        No time-series data available for {rangeLabel}.
      </div>
    {/if}

    <DataTable {columns} rows={rows} {cellRenderers} />
  {/if}
</section>
