<script lang="ts">
  import { fetchJSON } from '$lib/api';
  import { DataTable, ErrorState, SkeletonTable, StatusBadge } from '$lib/components';
  import { loadPhase } from '$lib/loadState';
  import type { HealthResponse, PricingResponse, ProviderAvailability, ProviderStatus } from '$lib/types';
  import type { Snippet } from 'svelte';

  type RowData = Record<string, string | number | boolean | null | undefined>;
  type ColumnDef = {
    key: string;
    label: string;
    sortable?: boolean;
  };
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
  let passwordError = $state<string | null>(null);
  let passwordSuccess = $state<string | null>(null);
  let version = $state('unknown');
  let uptimeSeconds = $state(0);
  let storageBackend = $state('unknown');
  let providerRows = $state<ProviderStatus[]>([]);
  let pricingRows = $state<PricingRow[]>([]);
  let currentPassword = $state('');
  let newPassword = $state('');
  let confirmPassword = $state('');

  const phase = $derived(loadPhase({ loading, errorMessage, hasData: true }));

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

  function providerBadgeStatus(status: ProviderAvailability): 'success' | 'warning' | 'error' {
    if (status === 'operational') return 'success';
    if (status === 'degraded') return 'warning';
    return 'error';
  }

  function providerStatusLabel(row: ProviderStatus): string {
    if (row.detail.startsWith('Checking')) return 'Checking';
    if (row.status === 'operational') return 'Operational';
    if (row.status === 'degraded') return 'Degraded';
    if (row.status === 'outage') return 'Outage';
    return 'Status unavailable';
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

      providerRows = health.providers;

    } catch (err) {
      errorMessage = err instanceof Error ? err.message : 'Failed to load system health.';
      loading = false;
      return;
    }

    loading = false;
    await loadPricing();
  }

  /**
   * loadPricing is split out from loadSettings so a pricing failure — and its
   * retry — never touches the page-wide `loading` flag. That flag hides the
   * whole page behind the skeleton, which would unmount the password form the
   * user might already be filling in.
   */
  async function loadPricing(): Promise<void> {
    pricingWarning = null;

    try {
      const pricing = await fetchJSON<PricingResponse>('/pricing');
      pricingRows = pricing.pricing.map((entry) => ({
        model: entry.model,
        provider: entry.provider,
        input_per_million: entry.input_per_million,
        output_per_million: entry.output_per_million
      }));
    } catch (err) {
      pricingRows = [];
      pricingWarning = err instanceof Error ? err.message : 'Pricing data unavailable.';
    }
  }

  async function changePassword(): Promise<void> {
    passwordError = null;
    passwordSuccess = null;

    try {
      await fetchJSON('/settings/password', {
        method: 'PUT',
        body: JSON.stringify({
          current_password: currentPassword,
          new_password: newPassword,
          confirm_password: confirmPassword
        })
      });
      passwordSuccess = 'Password updated.';
      currentPassword = '';
      newPassword = '';
      confirmPassword = '';
    } catch (err) {
      passwordError = err instanceof Error ? err.message : 'Failed to update password.';
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

  {#if phase === 'error'}
    <ErrorState message={errorMessage ?? 'Failed to load system health.'} onRetry={loadSettings} />
  {/if}

  {#if phase === 'loading'}
    <div class="space-y-4">
      <SkeletonTable rows={3} columns={3} label="Loading system info" />
      <SkeletonTable rows={4} columns={4} label="Loading pricing" />
    </div>
  {:else if phase === 'ready'}
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
      <p class="mt-1 text-sm text-text-secondary">
        Public service availability only — not a test of API access or credentials.
      </p>
      <div class="mt-3 grid grid-cols-1 gap-2 md:grid-cols-3">
        {#each providerRows as provider}
          <div class="rounded-md border border-border-default bg-elevated px-3 py-2" title={provider.detail}>
            <div class="flex items-center justify-between gap-3">
              <span class="text-sm text-text-primary">{provider.label}</span>
              <StatusBadge status={providerBadgeStatus(provider.status)} />
            </div>
            <p class="mt-1 text-xs text-text-secondary">{providerStatusLabel(provider)}</p>
          </div>
        {/each}
      </div>
    </section>

    <section class="rounded-lg border border-border-default bg-surface p-4">
      <h2 class="text-lg font-semibold text-text-primary">Model Pricing</h2>
      {#if pricingWarning}
        <div
          role="status"
          class="mt-2 flex items-center justify-between gap-4 rounded-md border border-warning/40 bg-warning/10 px-3 py-2"
        >
          <p class="text-sm text-warning">{pricingWarning}</p>
          <button
            type="button"
            class="rounded-md border border-border-default bg-elevated px-2.5 py-1 text-xs font-medium text-text-primary hover:bg-accent hover:text-white"
            onclick={loadPricing}
          >
            Retry pricing
          </button>
        </div>
      {/if}
      {#if pricingRows.length === 0}
        <p class="mt-3 text-sm text-text-muted">No configured model pricing entries.</p>
      {:else}
        <div class="mt-3">
          <DataTable columns={pricingColumns} rows={pricingRows} cellRenderers={pricingRenderers} />
        </div>
      {/if}
    </section>

    <section class="rounded-lg border border-border-default bg-surface p-4">
      <h2 class="text-lg font-semibold text-text-primary">Change Password</h2>
      <form
        class="mt-3"
        onsubmit={(event) => {
          event.preventDefault();
          void changePassword();
        }}
      >
        <div class="grid grid-cols-1 gap-3 md:grid-cols-3">
          <label class="space-y-1">
            <span class="text-xs uppercase tracking-wide text-text-secondary">Current password</span>
            <input
              bind:value={currentPassword}
              type="password"
              class="w-full rounded-md border border-border-default bg-elevated px-3 py-2 text-sm text-text-primary outline-none ring-0"
            />
          </label>
          <label class="space-y-1">
            <span class="text-xs uppercase tracking-wide text-text-secondary">New password</span>
            <input
              bind:value={newPassword}
              type="password"
              class="w-full rounded-md border border-border-default bg-elevated px-3 py-2 text-sm text-text-primary outline-none ring-0"
            />
          </label>
          <label class="space-y-1">
            <span class="text-xs uppercase tracking-wide text-text-secondary">Confirm new password</span>
            <input
              bind:value={confirmPassword}
              type="password"
              class="w-full rounded-md border border-border-default bg-elevated px-3 py-2 text-sm text-text-primary outline-none ring-0"
            />
          </label>
        </div>
        {#if passwordError}
          <p class="mt-3 text-sm text-danger">{passwordError}</p>
        {/if}
        {#if passwordSuccess}
          <p class="mt-3 text-sm text-success">{passwordSuccess}</p>
        {/if}
        <button
          type="submit"
          class="mt-4 rounded-md bg-accent px-4 py-2 text-sm font-medium text-white hover:bg-accent-hover"
        >
          Update Password
        </button>
      </form>
    </section>
  {/if}
</section>
