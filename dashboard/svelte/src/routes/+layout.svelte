<script lang="ts">
  import '../app.css';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { fetchJSON } from '$lib/api';
  import type { AuthStatusResponse, HealthResponse } from '$lib/types';
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
  let displayVersion = $state('v0.1.0');

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

  onMount(async () => {
    await Promise.all([loadAuthStatus(), loadHealthVersion()]);
    await syncRoute();
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
        <p class="text-lg font-semibold tracking-tight">Oberwatch</p>
        <p class="text-xs text-text-secondary">{displayVersion}</p>
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
        <div class="mt-3 text-xs text-text-secondary">{displayVersion}</div>
      </div>
    </aside>

    <main class="ml-56 h-screen overflow-y-auto p-6">
      {@render children()}
    </main>
  </div>
{:else}
  {@render children()}
{/if}
