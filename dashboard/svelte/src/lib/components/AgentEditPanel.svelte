<script lang="ts">
  import type { Agent, Budget, BudgetUpdateRequest } from '$lib/types';

  let {
    open,
    agent,
    budget,
    busy = false,
    errorMessage = null,
    onClose,
    onSave
  }: {
    open: boolean;
    agent: Agent | null;
    budget: Budget | null;
    busy?: boolean;
    errorMessage?: string | null;
    onClose?: () => void;
    onSave?: (payload: { oldName: string; newName: string; budget: BudgetUpdateRequest }) => Promise<void>;
  } = $props();

  let name = $state('');
  let limitUSD = $state('0');
  let period = $state('daily');
  let actionOnExceed = $state('alert');
  let downgradeChain = $state('');
  let downgradeThreshold = $state('80');
  let alertThresholds = $state('50, 80, 100');

  $effect(() => {
    if (!open || !agent || !budget) {
      return;
    }
    name = agent.name;
    limitUSD = String(budget.limit_usd ?? 0);
    period = budget.period ?? 'daily';
    actionOnExceed = budget.action_on_exceed ?? 'alert';
    downgradeChain = (budget.downgrade_chain ?? []).join(', ');
    downgradeThreshold = String(budget.downgrade_threshold_pct ?? 80);
    alertThresholds = (budget.alert_thresholds_pct ?? [50, 80, 100]).join(', ');
  });

  function parseList(raw: string): string[] {
    return raw
      .split(',')
      .map((value) => value.trim())
      .filter(Boolean);
  }

  function parseThresholds(raw: string): number[] {
    return raw
      .split(',')
      .map((value) => Number(value.trim()))
      .filter((value) => !Number.isNaN(value));
  }

  async function submit(): Promise<void> {
    if (!agent || !onSave) {
      return;
    }
    await onSave({
      oldName: agent.name,
      newName: name.trim(),
      budget: {
        limit_usd: Number(limitUSD) || 0,
        period,
        action_on_exceed: actionOnExceed,
        downgrade_chain: parseList(downgradeChain),
        downgrade_threshold_pct: Number(downgradeThreshold) || 0,
        alert_thresholds_pct: parseThresholds(alertThresholds)
      }
    });
  }
</script>

{#if open}
  <button
    type="button"
    aria-label="Close edit panel"
    class="fixed inset-0 z-40 bg-black/60"
    disabled={busy}
    onclick={() => !busy && onClose?.()}
  ></button>
  <aside class="fixed inset-y-0 right-0 z-50 w-full max-w-xl overflow-y-auto border-l border-border-default bg-surface p-6 shadow-2xl">
    <div class="flex items-start justify-between gap-4">
      <div>
        <h2 class="text-xl font-semibold text-text-primary">Edit Agent</h2>
        <p class="mt-1 text-sm text-text-secondary">Rename the agent and update its runtime budget policy.</p>
      </div>
      <button type="button" class="text-sm text-text-secondary hover:text-text-primary" onclick={() => !busy && onClose?.()}>
        Close
      </button>
    </div>

    <div class="mt-6 space-y-4">
      <label class="block space-y-2">
        <span class="text-xs font-medium uppercase tracking-wide text-text-secondary">Agent Name</span>
        <input class="w-full rounded-md border border-border-default bg-elevated px-3 py-2 text-sm text-text-primary" bind:value={name} />
      </label>

      <label class="block space-y-2">
        <span class="text-xs font-medium uppercase tracking-wide text-text-secondary">Budget Limit (USD)</span>
        <input class="w-full rounded-md border border-border-default bg-elevated px-3 py-2 text-sm text-text-primary" bind:value={limitUSD} placeholder="0 = unlimited" type="number" min="0" step="0.01" />
      </label>

      <label class="block space-y-2">
        <span class="text-xs font-medium uppercase tracking-wide text-text-secondary">Period</span>
        <select class="w-full rounded-md border border-border-default bg-elevated px-3 py-2 text-sm text-text-primary" bind:value={period}>
          <option value="hourly">Hourly</option>
          <option value="daily">Daily</option>
          <option value="weekly">Weekly</option>
          <option value="monthly">Monthly</option>
        </select>
      </label>

      <label class="block space-y-2">
        <span class="text-xs font-medium uppercase tracking-wide text-text-secondary">Action on Exceed</span>
        <select class="w-full rounded-md border border-border-default bg-elevated px-3 py-2 text-sm text-text-primary" bind:value={actionOnExceed}>
          <option value="reject">Reject</option>
          <option value="downgrade">Downgrade</option>
          <option value="alert">Alert</option>
          <option value="kill">Kill</option>
        </select>
      </label>

      <label class="block space-y-2">
        <span class="text-xs font-medium uppercase tracking-wide text-text-secondary">Downgrade Chain</span>
        <input class="w-full rounded-md border border-border-default bg-elevated px-3 py-2 text-sm text-text-primary" bind:value={downgradeChain} />
        <p class="text-xs text-text-muted">Models to fall back to, in order, when budget threshold is reached.</p>
      </label>

      <label class="block space-y-2">
        <span class="text-xs font-medium uppercase tracking-wide text-text-secondary">Downgrade Threshold (%)</span>
        <input class="w-full rounded-md border border-border-default bg-elevated px-3 py-2 text-sm text-text-primary" bind:value={downgradeThreshold} type="number" min="0" max="100" step="1" />
      </label>

      <label class="block space-y-2">
        <span class="text-xs font-medium uppercase tracking-wide text-text-secondary">Alert Thresholds (%)</span>
        <input class="w-full rounded-md border border-border-default bg-elevated px-3 py-2 text-sm text-text-primary" bind:value={alertThresholds} />
      </label>

      {#if errorMessage}
        <div class="rounded-md border border-danger/40 bg-danger/10 px-3 py-2 text-sm text-danger">{errorMessage}</div>
      {/if}
    </div>

    <div class="mt-8 flex items-center justify-end gap-3">
      <button type="button" class="rounded-md border border-border-default bg-elevated px-4 py-2 text-sm text-text-primary" disabled={busy} onclick={() => onClose?.()}>
        Cancel
      </button>
      <button type="button" class="rounded-md bg-accent px-4 py-2 text-sm font-medium text-white disabled:opacity-60" disabled={busy} onclick={submit}>
        Save
      </button>
    </div>
  </aside>
{/if}
