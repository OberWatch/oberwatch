<script lang="ts">
  import { onMount } from 'svelte';
  import { fetchJSON } from '$lib/api';
  import { formatUSD } from '$lib/currency';
  import { AgentEditPanel, BudgetBar, DataTable, ErrorState, SkeletonTable, StatusBadge } from '$lib/components';
  import { loadPhase } from '$lib/loadState';
  import type { Budget, BudgetUpdateRequest, BudgetsResponse, Agent, AgentsResponse } from '$lib/types';
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
    lastModel: string;
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
    { key: 'lastModel', label: 'Model', sortable: true },
    { key: 'spentUSD', label: 'Spend Today', sortable: true },
    { key: 'limitUSD', label: 'Budget Limit', sortable: true },
    { key: 'usage', label: 'Usage', sortable: true },
    { key: 'lastSeenAt', label: 'Last Seen', sortable: true },
    { key: 'actions', label: 'Actions' }
  ];

  let loading = $state(true);
  let errorMessage = $state<string | null>(null);
  let successMessage = $state<string | null>(null);
  let search = $state('');
  let rows = $state<AgentRow[]>([]);
  let agents = $state<Agent[]>([]);
  let modelsByAgent = $state<Record<string, string[]>>({});
  let budgetsByAgent = $state<Record<string, Budget>>({});
  let actionBusyByAgent = $state<Record<string, boolean>>({});
  let proxyURL = $state('');
  let selectedAgent = $state<Agent | null>(null);
  let selectedBudget = $state<Budget | null>(null);
  let editOpen = $state(false);
  let editBusy = $state(false);
  let editError = $state<string | null>(null);

  const phase = $derived(loadPhase({ loading, errorMessage, hasData: rows.length > 0 }));

  const filteredRows = $derived.by(() => {
    const term = search.trim().toLowerCase();
    if (!term) {
      return rows;
    }
    return rows.filter((row) => row.name.toLowerCase().includes(term));
  });

  const cellRenderers = $derived.by<Record<string, Snippet<[RowData]>>>(() => ({
    status: statusCell,
    lastModel: modelCell,
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

      agents = agentsRes.agents;
      modelsByAgent = Object.fromEntries(
        agentsRes.agents.map((agent: Agent) => [agent.name, agent.models_used])
      );
      const budgetMap = Object.fromEntries(
        budgetsRes.budgets.map((budget: Budget) => [budget.agent, budget])
      );
      budgetsByAgent = budgetMap;

      rows = agentsRes.agents.map((agent: Agent) => {
        const budget = budgetMap[agent.name] as Budget | undefined;
        const spentUSD = budget?.spent_usd ?? agent.total_cost_usd;
        const limitUSD = budget?.limit_usd ?? 0;
        const usage = budget?.percentage_used ?? 0;
        const status = budget?.status ?? agent.status;

        return {
          name: agent.name,
          status,
          lastModel: agent.last_model,
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

  function openEditor(agentName: string): void {
    const agent = agents.find((entry) => entry.name === agentName) ?? null;
    const budget = budgetsByAgent[agentName] ?? null;
    selectedAgent = agent;
    selectedBudget = budget;
    editError = null;
    editOpen = true;
  }

  async function saveAgentEdit(payload: {
    oldName: string;
    newName: string;
    budget: BudgetUpdateRequest;
  }): Promise<void> {
    editBusy = true;
    editError = null;

    try {
      const nextName = payload.newName.trim();
      if (nextName !== payload.oldName) {
        await fetchJSON(`/agents/${encodeURIComponent(payload.oldName)}/rename`, {
          method: 'PUT',
          body: JSON.stringify({ new_name: nextName })
        });
      }

      await fetchJSON(`/budgets/${encodeURIComponent(nextName)}`, {
        method: 'PUT',
        body: JSON.stringify(payload.budget)
      });

      successMessage = `Updated agent "${nextName}".`;
      editOpen = false;
      selectedAgent = null;
      selectedBudget = null;
      await loadAgents();
    } catch (err) {
      editError = err instanceof Error ? err.message : 'Failed to update agent.';
    } finally {
      editBusy = false;
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

{#snippet modelCell(raw: RowData)}
  {@const row = raw as AgentRow}
  {@const modelsUsed = modelsByAgent[row.name] ?? []}
  <div class="font-mono text-[13px]">
    <span class:text-text-muted={!row.lastModel}>{row.lastModel || '—'}</span>
    {#if modelsUsed.length > 1}
      <details class="mt-1 text-xs text-text-secondary">
        <summary class="w-fit cursor-pointer rounded-sm text-accent focus:outline-none focus:ring-2 focus:ring-accent">
          All {modelsUsed.length} models
        </summary>
        <ul class="mt-1 space-y-1" aria-label={`Models used by ${row.name}`}>
          {#each modelsUsed as model}
            <li>{model}</li>
          {/each}
        </ul>
      </details>
    {/if}
  </div>
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
    <button
      type="button"
      class="rounded-md border border-border-default bg-elevated px-2.5 py-1 text-xs font-medium text-text-primary"
      onclick={() => openEditor(row.name)}
    >
      Edit
    </button>
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

  {#if phase === 'error'}
    <ErrorState message={errorMessage ?? 'Failed to load agents.'} onRetry={loadAgents} />
  {/if}

  {#if successMessage}
    <div class="rounded-lg border border-success/40 bg-success/10 p-4">
      <p class="text-sm text-success">{successMessage}</p>
    </div>
  {/if}

  {#if phase === 'loading'}
    <SkeletonTable rows={6} columns={8} label="Loading agents" />
  {:else if phase === 'empty'}
    <div class="space-y-4 rounded-lg border border-border-default bg-surface p-6">
      <div class="space-y-1">
        <h2 class="text-lg font-semibold text-text-primary">No agents detected yet</h2>
        <p class="text-sm text-text-secondary">
          Point your AI agents at Oberwatch to start tracking spend and controls.
        </p>
      </div>

      <div class="overflow-hidden rounded-2xl border border-border-default bg-elevated">
        <div class="flex items-center gap-3 border-b border-border-default px-4 py-3">
          <div class="flex items-center gap-2">
            <span class="h-3 w-3 rounded-full bg-danger"></span>
            <span class="h-3 w-3 rounded-full bg-warning"></span>
            <span class="h-3 w-3 rounded-full bg-success"></span>
          </div>
          <p class="font-mono text-sm text-text-secondary">point any agent at Oberwatch</p>
        </div>

        <pre class="overflow-x-auto px-4 py-5 font-mono text-sm leading-7 text-text-primary/85"><code><span class="text-text-secondary/85"># Just change the base URL and add a header</span>
curl <span class="text-danger">{proxyURL}</span><span class="text-accent">/v1/chat/completions</span> \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "Content-Type: application/json" \
  -H "X-Oberwatch-Agent: research-agent" \
  -d '&#123;
    "model": "gpt-4.1-mini",
    "messages": [&#123;"role": "user", "content": "Hello"&#125;]
  &#125;'</code></pre>
      </div>
    </div>
  {:else if phase === 'ready'}
    {#if filteredRows.length === 0}
      <div class="rounded-lg border border-border-default bg-surface p-8 text-center text-sm text-text-muted">
        No agents match the current filter.
      </div>
    {:else}
      <DataTable {columns} rows={filteredRows} {onSort} {cellRenderers} />
    {/if}
  {/if}
</section>

<AgentEditPanel
  open={editOpen}
  agent={selectedAgent}
  budget={selectedBudget}
  busy={editBusy}
  errorMessage={editError}
  onClose={() => {
    editOpen = false;
    editError = null;
  }}
  onSave={saveAgentEdit}
/>
