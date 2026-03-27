<script lang="ts">
  import '../app.css';
  import { page } from '$app/state';
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

  function isActive(pathname: string, href: string): boolean {
    if (href === '/') {
      return pathname === '/';
    }
    return pathname.startsWith(href);
  }
</script>

<div class="h-screen overflow-hidden bg-base text-text-primary">
  <aside class="fixed inset-y-0 left-0 z-20 flex w-56 flex-col border-r border-border-default bg-surface px-4 py-5">
    <div class="mb-8 border-b border-border-default pb-4">
      <p class="text-lg font-semibold tracking-tight">Oberwatch</p>
      <p class="text-xs text-text-secondary">v0.1.0</p>
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

    <div class="border-t border-border-default pt-4 text-xs text-text-secondary">v0.1.0</div>
  </aside>

  <main class="ml-56 h-screen overflow-y-auto p-6">
    {@render children()}
  </main>
</div>
