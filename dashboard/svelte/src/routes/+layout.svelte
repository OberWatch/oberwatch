<script lang="ts">
  import '../app.css';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { ApiError, fetchJSON } from '$lib/api';
  import { connectStream } from '$lib/sse';
  import {
    UPGRADE_POLL_INTERVAL_MS,
    upgradeConfirmationLines,
    upgradeFailureMessage,
    upgradePollDecision,
    upgradeResultNote,
    upgradeView
  } from '$lib/upgrade';
  import type {
    AuthStatusResponse,
    BudgetAlertEvent,
    HealthResponse,
    UpgradeStartResponse,
    UpgradeStatusResponse
  } from '$lib/types';
  import { onMount } from 'svelte';
  import type { Snippet } from 'svelte';

  let { children }: { children: Snippet } = $props();

  type NavItem = {
    href: string;
    label: string;
    disabled?: boolean;
    tooltip?: string;
  };

  const navItems: NavItem[] = [
    { href: '/', label: 'Overview' },
    { href: '/agents', label: 'Agents' },
    { href: '/costs', label: 'Costs' },
    { href: '/traces', label: 'Traces', disabled: true, tooltip: 'Coming in v0.2' },
    { href: '/tests', label: 'Tests', disabled: true, tooltip: 'Coming in v0.3' },
    { href: '/settings', label: 'Settings' }
  ];

  const active = $derived(page.url.pathname);
  const isAuthRoute = $derived(active === '/login' || active === '/setup');

  let authLoading = $state(true);
  let authStatus = $state<AuthStatusResponse | null>(null);
  let logoutError = $state<string | null>(null);
  let displayVersion = $state('v0.1.3');
  let emergencyStop = $state(false);
  let emergencyBusy = $state(false);
  let alertToasts = $state<{ id: number; agent: string; threshold: number }[]>([]);
  let nextToastID = 1;

  type UpgradePhase = 'idle' | 'confirming' | 'applying' | 'waiting' | 'failed';

  let upgradeStatus = $state<UpgradeStatusResponse | null>(null);
  let upgradePhase = $state<UpgradePhase>('idle');
  let upgradeError = $state<string | null>(null);
  let upgradeTarget = $state('');
  let upgradeWaitTimedOut = $state(false);

  const upgrade = $derived(upgradeView(upgradeStatus));
  const upgradeResult = $derived(upgradeResultNote(upgradeStatus?.last_result));
  const upgradeConfirmation = $derived(
    upgradeConfirmationLines(upgradeTarget || upgradeStatus?.latest_version || '')
  );
  const upgradeBusy = $derived(upgradePhase === 'applying' || upgradePhase === 'waiting');

  function isActive(pathname: string, href: string): boolean {
    if (href === '/') {
      return pathname === '/';
    }
    return pathname.startsWith(href);
  }

  async function loadAuthStatus(): Promise<void> {
    try {
      authStatus = await fetchJSON<AuthStatusResponse>('/auth/status');
    } finally {
      authLoading = false;
    }
  }

  async function loadHealthVersion(): Promise<void> {
    try {
      const health = await fetchJSON<HealthResponse>('/health');
      displayVersion = health.version;
      emergencyStop = health.emergency_stop ?? false;
    } catch {
      // Keep the default sidebar version if health is temporarily unavailable.
    }
  }

  async function syncRoute(): Promise<void> {
    if (authLoading || authStatus === null) return;

    if (!authStatus.setup_complete) {
      if (active !== '/setup') {
        await goto('/setup');
      }
      return;
    }

    if (!authStatus.authenticated) {
      if (active !== '/login') {
        await goto('/login');
      }
      return;
    }

    if (isAuthRoute) {
      await goto('/');
    }
  }

  async function logout(): Promise<void> {
    logoutError = null;

    try {
      await fetchJSON('/logout', { method: 'POST' });
      window.location.assign('/login');
    } catch (error) {
      logoutError = error instanceof Error ? error.message : 'Failed to sign out.';
    }
  }

  /**
   * A failed read is not an outcome: during the restart the server is briefly
   * down, and blanking the footer or reporting a failure would claim something
   * that did not happen. The last known status stays on screen instead.
   */
  async function loadUpgradeStatus(): Promise<UpgradeStatusResponse | null> {
    try {
      const next = await fetchJSON<UpgradeStatusResponse>('/upgrade/status');
      upgradeStatus = next;
      return next;
    } catch {
      return null;
    }
  }

  function beginUpgrade(): void {
    upgradeError = null;
    upgradeWaitTimedOut = false;
    upgradeTarget = upgradeStatus?.latest_version ?? '';
    upgradePhase = 'confirming';
  }

  function cancelUpgrade(): void {
    upgradePhase = 'idle';
  }

  /**
   * The request carries no body. There is no supported way to name a version,
   * tag, URL or path: the release installed is the one the server's own check
   * returned.
   */
  async function confirmUpgrade(): Promise<void> {
    upgradePhase = 'applying';
    upgradeError = null;
    upgradeWaitTimedOut = false;

    try {
      const started = await fetchJSON<UpgradeStartResponse>('/upgrade', { method: 'POST' });
      upgradeTarget = started.tag;
      upgradePhase = 'waiting';
      await waitForRestart(started.tag);
    } catch (error) {
      // Only a real HTTP answer tells us the upgrade did not start. A dropped
      // connection is ambiguous — the restart itself can end the request — so
      // it is treated as "outcome not known yet" and waited on, rather than
      // reported as a failure that may not have happened.
      if (!(error instanceof ApiError)) {
        upgradePhase = 'waiting';
        await waitForRestart(upgradeTarget);
        return;
      }
      upgradeError = upgradeFailureMessage(error.details?.error?.code, error.message);
      upgradePhase = 'failed';
      await loadUpgradeStatus();
    }
  }

  async function waitForRestart(target: string): Promise<void> {
    const startedAt = Date.now();

    for (;;) {
      await sleep(UPGRADE_POLL_INTERVAL_MS);
      const next = await loadUpgradeStatus();
      const decision = upgradePollDecision({ status: next, target, elapsedMs: Date.now() - startedAt });
      if (decision === 'continue') continue;

      upgradeWaitTimedOut = decision === 'timeout';
      upgradePhase = 'idle';
      if (decision === 'done') {
        await loadHealthVersion();
      }
      return;
    }
  }

  function sleep(milliseconds: number): Promise<void> {
    return new Promise((resolve) => {
      window.setTimeout(resolve, milliseconds);
    });
  }

  async function resumeOperations(): Promise<void> {
    emergencyBusy = true;
    try {
      await fetchJSON('/resume', { method: 'POST' });
      await loadHealthVersion();
    } finally {
      emergencyBusy = false;
    }
  }

  function showBudgetAlertToast(event: BudgetAlertEvent): void {
    const threshold = Math.round(event.threshold_pct ?? 0);
    const id = nextToastID++;
    alertToasts = [...alertToasts, { id, agent: event.agent || 'unknown', threshold }];

    window.setTimeout(() => {
      alertToasts = alertToasts.filter((toast) => toast.id !== id);
    }, 5000);
  }

  onMount(() => {
    void (async () => {
      await Promise.all([loadAuthStatus(), loadHealthVersion()]);
      await syncRoute();
      // The upgrade status is authenticated, so it is only read once a session
      // is confirmed rather than on the login and setup screens.
      if (authStatus?.authenticated) {
        await loadUpgradeStatus();
      }
    })();

    const stream = connectStream((eventName, data) => {
      if (eventName === 'budget_alert') {
        showBudgetAlertToast(data as BudgetAlertEvent);
      }
      if (eventName === 'emergency_stop') {
        void loadHealthVersion();
      }
    });

    return () => {
      stream.close();
    };
  });

  $effect(() => {
    void syncRoute();
  });
</script>

{#if authLoading}
  <div class="flex h-screen items-center justify-center bg-base text-sm text-text-secondary">
    Checking session...
  </div>
{:else if authStatus?.authenticated && !isAuthRoute}
  <div class="h-screen overflow-hidden bg-base text-text-primary">
    <aside class="fixed inset-y-0 left-0 z-20 flex w-56 flex-col border-r border-border-default bg-surface px-4 py-5">
      <div class="mb-8 border-b border-border-default pb-4">
        <div class="flex items-center gap-3">
          <img src="/logo-white.svg" alt="Oberwatch logo" class="h-10 w-10 shrink-0" />
          <div>
            <p class="text-lg font-semibold tracking-tight">Oberwatch</p>
            <p class="text-xs text-text-secondary">{displayVersion}</p>
          </div>
        </div>
      </div>

      <nav class="flex flex-1 flex-col gap-1">
        {#each navItems as item}
          {#if item.disabled}
            <span
              class="cursor-not-allowed rounded-md px-3 py-2 text-sm text-text-muted"
              title={item.tooltip}
            >
              {item.label}
            </span>
          {:else}
            <a
              href={item.href}
              class={`rounded-md px-3 py-2 text-sm transition-colors ${isActive(active, item.href)
                ? 'bg-accent/20 text-accent'
                : 'text-text-secondary hover:bg-elevated hover:text-text-primary'}`}
            >
              {item.label}
            </a>
          {/if}
        {/each}
      </nav>

      <div class="border-t border-border-default pt-4">
        <button
          type="button"
          class="w-full rounded-md px-3 py-2 text-left text-sm text-text-secondary transition-colors hover:bg-elevated hover:text-text-primary"
          onclick={logout}
        >
          Logout
        </button>
        {#if logoutError}
          <p class="mt-2 text-xs text-danger">{logoutError}</p>
        {/if}

        <div class="mt-3 flex items-center justify-between gap-2">
          <span class="text-xs text-text-secondary">{displayVersion}</span>

          {#if upgradeBusy}
            <span class="text-xs text-text-secondary" role="status" aria-busy="true">
              {upgradePhase === 'applying' ? 'Verifying release' : 'Restarting'}
            </span>
          {:else if upgrade.kind === 'available' && upgradePhase !== 'confirming'}
            <button
              type="button"
              class="rounded-md border border-accent/40 px-2 py-1 text-xs font-medium text-accent transition-colors hover:bg-accent/20 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
              onclick={beginUpgrade}
            >
              {upgrade.action}
            </button>
          {:else if upgrade.kind === 'checking'}
            <span class="text-xs text-text-muted">{upgrade.note}</span>
          {:else if upgrade.kind === 'unavailable'}
            <span class="text-xs text-text-muted" title="The release check could not be completed.">
              {upgrade.note}
            </span>
          {/if}
        </div>

        {#if upgradePhase === 'confirming'}
          <div class="mt-3 rounded-md border border-border-default bg-elevated p-3">
            <p class="text-xs font-semibold text-text-primary">Install {upgradeTarget}?</p>
            <ul class="mt-2 list-disc space-y-1 pl-4 text-xs text-text-secondary">
              {#each upgradeConfirmation as line}
                <li>{line}</li>
              {/each}
            </ul>
            <div class="mt-3 flex gap-2">
              <button
                type="button"
                class="rounded-md bg-accent px-2 py-1 text-xs font-medium text-white focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
                onclick={confirmUpgrade}
              >
                Install and restart
              </button>
              <button
                type="button"
                class="rounded-md border border-border-default px-2 py-1 text-xs text-text-secondary focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
                onclick={cancelUpgrade}
              >
                Cancel
              </button>
            </div>
          </div>
        {/if}

        {#if upgrade.kind === 'unsupported'}
          <p class="mt-2 text-xs text-text-muted" role="status">{upgrade.note}</p>
        {/if}

        <div aria-live="polite">
          {#if upgradeError}
            <p class="mt-2 text-xs text-danger">{upgradeError}</p>
          {/if}
          {#if upgradeWaitTimedOut}
            <p class="mt-2 text-xs text-warning">
              The service has not come back yet. Reload the dashboard to see the result.
            </p>
          {/if}
          {#if upgradeResult && upgradePhase === 'idle'}
            <p
              class={`mt-2 text-xs ${upgradeResult.tone === 'success'
                ? 'text-success'
                : upgradeResult.tone === 'warning'
                  ? 'text-warning'
                  : 'text-danger'}`}
            >
              {upgradeResult.text}
            </p>
          {/if}
        </div>
      </div>
    </aside>

    <main class="ml-56 h-screen overflow-y-auto p-6">
      {#if alertToasts.length > 0}
        <div class="pointer-events-none fixed right-6 top-6 z-50 flex w-full max-w-sm flex-col gap-3">
          {#each alertToasts as toast (toast.id)}
            <div class="rounded-lg border border-warning/40 bg-surface/95 px-4 py-3 shadow-2xl backdrop-blur">
              <p class="text-sm font-semibold text-warning">Budget Threshold Reached</p>
              <p class="mt-1 text-sm text-text-primary">
                Agent <span class="font-medium">{toast.agent}</span> crossed {toast.threshold}% of its budget.
              </p>
            </div>
          {/each}
        </div>
      {/if}
      {#if emergencyStop}
        <div class="mb-4 flex items-center justify-between gap-4 rounded-lg border border-warning/40 bg-warning/10 px-4 py-3">
          <p class="text-sm font-medium text-warning">Emergency Stop Active — All agent requests are paused.</p>
          <button
            type="button"
            class="rounded-md bg-success px-3 py-2 text-sm font-medium text-white disabled:opacity-60"
            disabled={emergencyBusy}
            onclick={resumeOperations}
          >
            Resume Operations
          </button>
        </div>
      {/if}
      {@render children()}
    </main>
  </div>
{:else}
  {@render children()}
{/if}
