<script lang="ts">
  import { fetchJSON } from '$lib/api';
  import { DataTable, StatusBadge } from '$lib/components';
  import type { HealthResponse, PricingResponse } from '$lib/types';
  import type { Snippet } from 'svelte';

  type RowData = Record<string, string | number | boolean | null | undefined>;
  type ColumnDef = {
    key: string;
    label: string;
    sortable?: boolean;
  };
  type ProviderRow = RowData & { provider: string; status: string };
  type PricingRow = RowData & {
    model: string;
    provider: string;
    input_per_million: number;
    output_per_million: number;
  };

  const pricingColumns: ColumnDef[] = [
    { key: 'model', label: 'Model', sortable: true },
    { key: 'provider', label: 'Provider', sortable: true },
    { key: 'input_per_million', label: 'Input / 1M', sortable: true },
    { key: 'output_per_million', label: 'Output / 1M', sortable: true }
  ];

  let loading = $state(true);
  let errorMessage = $state<string | null>(null);
  let pricingWarning = $state<string | null>(null);
  let version = $state('unknown');
  let uptimeSeconds = $state(0);
  let storageBackend = $state('unknown');
  let providerRows = $state<ProviderRow[]>([]);
  let pricingRows = $state<PricingRow[]>([]);

  const pricingRenderers = $derived.by<Record<string, Snippet<[RowData]>>>(() => ({
    input_per_million: inputPriceCell,
    output_per_million: outputPriceCell
  }));

  function formatUptime(seconds: number): string {
    if (seconds < 60) return `${seconds}s`;
    const days = Math.floor(seconds / 86400);
    const hours = Math.floor((seconds % 86400) / 3600);
    const mins = Math.floor((seconds % 3600) / 60);
    if (days > 0) return `${days}d ${hours}h ${mins}m`;
    if (hours > 0) return `${hours}h ${mins}m`;
    return `${mins}m`;
  }

  function formatPrice(value: number): string {
    return new Intl.NumberFormat('en-US', {
      style: 'currency',
      currency: 'USD',
      minimumFractionDigits: 2,
      maximumFractionDigits: 2
    }).format(value);
  }

  async function loadSettings(): Promise<void> {
    loading = true;
    errorMessage = null;
    pricingWarning = null;
    pricingRows = [];
    providerRows = [];

    try {
      const health = await fetchJSON<HealthResponse>('/health');

      version = health.version;
      uptimeSeconds = health.uptime_seconds;
      storageBackend = health.storage_backend ?? 'unknown';

      providerRows = Object.entries(health.providers).map(([provider, status]) => ({
        provider,
        status
      }));

    } catch (err) {
      errorMessage = err instanceof Error ? err.message : 'Failed to load system health.';
      loading = false;
      return;
    }

    try {
      const pricing = await fetchJSON<PricingResponse>('/pricing');
      pricingRows = pricing.pricing.map((entry) => ({
        model: entry.model,
        provider: entry.provider,
        input_per_million: entry.input_per_million,
        output_per_million: entry.output_per_million
      }));
    } catch (err) {
      pricingWarning =
        err instanceof Error ? err.message : 'Pricing data unavailable without a valid admin token.';
    } finally {
      loading = false;
    }
  }

  void loadSettings();
</script>

{#snippet inputPriceCell(raw: RowData)}
  {@const row = raw as PricingRow}
  <span class="font-mono text-[13px]">{formatPrice(row.input_per_million)}</span>
{/snippet}

{#snippet outputPriceCell(raw: RowData)}
  {@const row = raw as PricingRow}
  <span class="font-mono text-[13px]">{formatPrice(row.output_per_million)}</span>
{/snippet}

<section class="space-y-4">
  <header class="space-y-1">
    <h1 class="text-2xl font-semibold text-text-primary">Settings</h1>
    <p class="text-sm text-text-secondary">Read-only system configuration and runtime health.</p>
  </header>

  {#if errorMessage}
    <div class="rounded-lg border border-danger/40 bg-danger/10 p-4">
      <p class="text-sm text-danger">{errorMessage}</p>
      <button
        type="button"
        class="mt-3 rounded-md bg-accent px-3 py-1.5 text-xs font-medium text-white hover:bg-accent-hover"
        onclick={loadSettings}
      >
        Retry
      </button>
    </div>
  {/if}

  {#if loading}
    <div class="h-28 animate-pulse rounded-lg border border-border-default bg-surface"></div>
  {:else}
    <section class="rounded-lg border border-border-default bg-surface p-4">
      <h2 class="text-lg font-semibold text-text-primary">System Info</h2>
      <dl class="mt-3 grid grid-cols-1 gap-3 text-sm md:grid-cols-3">
        <div>
          <dt class="text-xs font-medium uppercase tracking-wide text-text-secondary">Version</dt>
          <dd class="mt-1 text-text-primary">{version}</dd>
        </div>
        <div>
          <dt class="text-xs font-medium uppercase tracking-wide text-text-secondary">Uptime</dt>
          <dd class="mt-1 text-text-primary">{formatUptime(uptimeSeconds)}</dd>
        </div>
        <div>
          <dt class="text-xs font-medium uppercase tracking-wide text-text-secondary">Storage Backend</dt>
          <dd class="mt-1 text-text-primary">{storageBackend}</dd>
        </div>
      </dl>
    </section>

    <section class="rounded-lg border border-border-default bg-surface p-4">
      <h2 class="text-lg font-semibold text-text-primary">Provider Status</h2>
      <div class="mt-3 grid grid-cols-1 gap-2 md:grid-cols-3">
        {#each providerRows as provider}
          <div class="flex items-center justify-between rounded-md border border-border-default bg-elevated px-3 py-2">
            <span class="text-sm text-text-primary">{provider.provider}</span>
            <StatusBadge status={provider.status === 'reachable' ? 'success' : 'error'} />
          </div>
        {/each}
      </div>
    </section>

    <section class="rounded-lg border border-border-default bg-surface p-4">
      <h2 class="text-lg font-semibold text-text-primary">Model Pricing</h2>
      {#if pricingWarning}
        <p class="mt-2 text-sm text-warning">{pricingWarning}</p>
      {/if}
      {#if pricingRows.length === 0}
        <p class="mt-3 text-sm text-text-muted">No configured model pricing entries.</p>
      {:else}
        <div class="mt-3">
          <DataTable columns={pricingColumns} rows={pricingRows} cellRenderers={pricingRenderers} />
        </div>
      {/if}
    </section>
  {/if}
</section>
