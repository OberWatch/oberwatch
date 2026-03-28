<script lang="ts">
  import { onMount } from 'svelte';
  import { fetchJSON } from '$lib/api';
  import { formatUSD } from '$lib/currency';
  import { BudgetBar, DataTable, StatusBadge } from '$lib/components';
  import type { Budget, BudgetsResponse, Agent, AgentsResponse } from '$lib/types';
  import type { Snippet } from 'svelte';

  type BadgeStatus = 'active' | 'near_limit' | 'killed' | 'success' | 'error' | 'warning';
  type ColumnDef = {
    key: string;
    label: string;
    sortable?: boolean;
  };
  type RowData = Record<string, string | number | boolean | null | undefined>;

  type AgentRow = RowData & {
    name: string;
    status: string;
    spentUSD: number;
    limitUSD: number;
    usage: number;
    lastSeenRelative: string;
    lastSeenAt: string;
    isActive: boolean;
    isKilled: boolean;
  };

  const columns: ColumnDef[] = [
    { key: 'name', label: 'Agent Name', sortable: true },
    { key: 'status', label: 'Status' },
    { key: 'spentUSD', label: 'Spend Today', sortable: true },
    { key: 'limitUSD', label: 'Budget Limit', sortable: true },
    { key: 'usage', label: 'Usage', sortable: true },
    { key: 'lastSeenAt', label: 'Last Seen', sortable: true },
    { key: 'actions', label: 'Actions' }
  ];

  let loading = $state(true);
  let errorMessage = $state<string | null>(null);
  let search = $state('');
  let rows = $state<AgentRow[]>([]);
  let actionBusyByAgent = $state<Record<string, boolean>>({});
  let proxyURL = $state('');

  const filteredRows = $derived.by(() => {
    const term = search.trim().toLowerCase();
    if (!term) {
      return rows;
    }
    return rows.filter((row) => row.name.toLowerCase().includes(term));
  });

  const cellRenderers = $derived.by<Record<string, Snippet<[RowData]>>>(() => ({
    status: statusCell,
    spentUSD: spentCell,
    limitUSD: limitCell,
    usage: usageCell,
    lastSeenAt: lastSeenCell,
    actions: actionsCell
  }));

  function relativeTime(timestamp?: string): string {
    if (!timestamp) {
      return 'never';
    }
    const ms = new Date(timestamp).getTime();
    if (Number.isNaN(ms)) {
      return 'unknown';
    }

    const delta = Math.max(0, Math.floor((Date.now() - ms) / 1000));
    if (delta < 60) return `${delta}s ago`;
    if (delta < 3600) return `${Math.floor(delta / 60)}m ago`;
    if (delta < 86400) return `${Math.floor(delta / 3600)}h ago`;
    return `${Math.floor(delta / 86400)}d ago`;
  }

  function toBadgeStatus(status: string): BadgeStatus {
    if (status === 'active') return 'active';
    if (status === 'killed') return 'killed';
    if (status === 'near_limit') return 'near_limit';
    if (status === 'warning') return 'warning';
    if (status === 'error') return 'error';
    return 'success';
  }

  async function loadAgents(): Promise<void> {
    loading = true;
    errorMessage = null;

    try {
      const [agentsRes, budgetsRes] = await Promise.all([
        fetchJSON<AgentsResponse>('/agents'),
        fetchJSON<BudgetsResponse>('/budgets')
      ]);

      const budgetMap = new Map<string, Budget>(
        budgetsRes.budgets.map((budget: Budget) => [budget.agent, budget])
      );

      rows = agentsRes.agents.map((agent: Agent) => {
        const budget = budgetMap.get(agent.name);
        const spentUSD = budget?.spent_usd ?? agent.total_cost_usd;
        const limitUSD = budget?.limit_usd ?? 0;
        const usage = budget?.percentage_used ?? 0;
        const status = budget?.status ?? agent.status;

        return {
          name: agent.name,
          status,
          spentUSD,
          limitUSD,
          usage,
          lastSeenAt: agent.last_seen_at,
          lastSeenRelative: relativeTime(agent.last_seen_at),
          isActive: status === 'active',
          isKilled: status === 'killed'
        };
      });
    } catch (err) {
      errorMessage = err instanceof Error ? err.message : 'Failed to load agents.';
    } finally {
      loading = false;
    }
  }

  function isBusy(agentName: string): boolean {
    return actionBusyByAgent[agentName] ?? false;
  }

  async function executeAgentAction(agentName: string, action: 'kill' | 'enable' | 'reset'): Promise<void> {
    const question = `Confirm ${action} for agent "${agentName}"?`;
    if (!confirm(question)) {
      return;
    }

    actionBusyByAgent = { ...actionBusyByAgent, [agentName]: true };

    try {
      await fetchJSON(`/budgets/${encodeURIComponent(agentName)}/${action}`, {
        method: 'POST'
      });
      await loadAgents();
    } catch (err) {
      errorMessage = err instanceof Error ? err.message : `Failed to ${action} agent "${agentName}".`;
    } finally {
      actionBusyByAgent = { ...actionBusyByAgent, [agentName]: false };
    }
  }

  onMount(() => {
    proxyURL = window.location.origin;
    void loadAgents();
  });

  function onSort(): void {
    // Local sorting is already handled inside DataTable.
  }
</script>

{#snippet statusCell(raw: RowData)}
  {@const row = raw as AgentRow}
  <StatusBadge status={toBadgeStatus(row.status)} />
{/snippet}

{#snippet spentCell(raw: RowData)}
  {@const row = raw as AgentRow}
  <span class="font-mono text-[13px]">{formatUSD(row.spentUSD)}</span>
{/snippet}

{#snippet limitCell(raw: RowData)}
  {@const row = raw as AgentRow}
  <span class="font-mono text-[13px]">{formatUSD(row.limitUSD)}</span>
{/snippet}

{#snippet usageCell(raw: RowData)}
  {@const row = raw as AgentRow}
  <BudgetBar percentage={row.usage} spentUSD={row.spentUSD} limitUSD={row.limitUSD} />
{/snippet}

{#snippet lastSeenCell(raw: RowData)}
  {@const row = raw as AgentRow}
  <span class="text-sm text-text-secondary">{row.lastSeenRelative}</span>
{/snippet}

{#snippet actionsCell(raw: RowData)}
  {@const row = raw as AgentRow}
  <div class="flex items-center gap-2">
    {#if row.isActive}
      <button
        type="button"
        class="rounded-md bg-danger px-2.5 py-1 text-xs font-medium text-white disabled:cursor-not-allowed disabled:opacity-60"
        disabled={isBusy(row.name)}
        onclick={() => executeAgentAction(row.name, 'kill')}
      >
        Kill
      </button>
    {/if}
    {#if row.isKilled}
      <button
        type="button"
        class="rounded-md bg-success px-2.5 py-1 text-xs font-medium text-white disabled:cursor-not-allowed disabled:opacity-60"
        disabled={isBusy(row.name)}
        onclick={() => executeAgentAction(row.name, 'enable')}
      >
        Enable
      </button>
    {/if}
    <button
      type="button"
      class="rounded-md border border-border-default bg-elevated px-2.5 py-1 text-xs font-medium text-text-primary disabled:cursor-not-allowed disabled:opacity-60"
      disabled={isBusy(row.name)}
      onclick={() => executeAgentAction(row.name, 'reset')}
    >
      Reset
    </button>
  </div>
{/snippet}

<section class="space-y-4">
  <header class="space-y-1">
    <h1 class="text-2xl font-semibold text-text-primary">Agents</h1>
    <p class="text-sm text-text-secondary">Manage budget state and actions per agent.</p>
  </header>

  <div class="rounded-lg border border-border-default bg-surface p-3">
    <input
      type="search"
      class="w-full rounded-md border border-border-default bg-elevated px-3 py-2 text-sm text-text-primary placeholder:text-text-muted focus:border-accent focus:outline-none"
      placeholder="Filter by agent name"
      bind:value={search}
    />
  </div>

  {#if errorMessage}
    <div class="rounded-lg border border-danger/40 bg-danger/10 p-4">
      <p class="text-sm text-danger">{errorMessage}</p>
      <button
        type="button"
        class="mt-3 rounded-md bg-accent px-3 py-1.5 text-xs font-medium text-white hover:bg-accent-hover"
        onclick={() => loadAgents()}
      >
        Retry
      </button>
    </div>
  {/if}

  {#if loading}
    <div class="overflow-hidden rounded-lg border border-border-default bg-surface">
      {#each Array.from({ length: 6 }) as _, index (index)}
        <div class="h-12 animate-pulse border-b border-border-default bg-elevated/30"></div>
      {/each}
    </div>
  {:else if filteredRows.length === 0}
    {#if rows.length === 0}
      <div class="rounded-lg border border-border-default bg-surface p-8 text-center text-sm text-text-secondary">
        No agents detected yet. Point your AI agents at <span class="font-mono">{proxyURL}</span> to get
        started.
      </div>
    {:else}
      <div class="rounded-lg border border-border-default bg-surface p-8 text-center text-sm text-text-muted">
        No agents match the current filter.
      </div>
    {/if}
  {:else}
    <DataTable {columns} rows={filteredRows} {onSort} {cellRenderers} />
  {/if}
</section>
