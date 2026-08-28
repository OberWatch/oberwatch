<script lang="ts">
  import { fetchJSON } from '$lib/api';
  import type { HealthResponse } from '$lib/types';
  import { onMount } from 'svelte';

  let username = $state('admin');
  let password = $state('');
  let errorMessage = $state<string | null>(null);
  let version = $state('v0.1.4');

  async function signIn(): Promise<void> {
    errorMessage = null;

    try {
      await fetchJSON('/login', {
        method: 'POST',
        body: JSON.stringify({ username, password })
      });
      window.location.assign('/');
    } catch (error) {
      errorMessage = error instanceof Error ? error.message : 'Invalid credentials.';
    }
  }

  onMount(() => {
    void (async () => {
      try {
        const health = await fetchJSON<HealthResponse>('/health');
        version = health.version;
      } catch {
        // Keep the default version label if the health endpoint is unavailable.
      }
    })();
  });
</script>

<div class="flex min-h-screen items-center justify-center bg-base px-6">
  <div class="w-full max-w-md space-y-8">
    <div class="flex items-center justify-center gap-4">
      <img src="/logo-white.svg" alt="Oberwatch logo" class="h-[5.5rem] w-[5.5rem] shrink-0" />
      <p class="text-xl font-semibold uppercase tracking-[0.35em] text-accent">Oberwatch</p>
    </div>

    <section class="rounded-2xl border border-border-default bg-surface p-8 shadow-lg shadow-black/20">
      <div class="space-y-3 text-center">
        <h1 class="text-3xl font-semibold text-text-primary">Sign in</h1>
        <p class="text-sm text-text-secondary">Use your admin account to access the dashboard.</p>
      </div>

      <form
        class="mt-8 space-y-6"
      onsubmit={(event) => {
        event.preventDefault();
        void signIn();
      }}
      >
      <label class="block space-y-2">
        <span class="text-xs uppercase tracking-wide text-text-secondary">Username</span>
        <input
          bind:value={username}
          type="text"
          class="w-full rounded-md border border-border-default bg-elevated px-3 py-2 text-sm text-text-primary outline-none"
        />
      </label>

      <label class="block space-y-2">
        <span class="text-xs uppercase tracking-wide text-text-secondary">Password</span>
        <input
          bind:value={password}
          type="password"
          class="w-full rounded-md border border-border-default bg-elevated px-3 py-2 text-sm text-text-primary outline-none"
        />
      </label>

      {#if errorMessage}
        <p class="text-center text-sm text-danger">{errorMessage}</p>
      {/if}

        <div class="pt-2 text-center">
          <button
            type="submit"
            class="min-w-36 rounded-md bg-accent px-6 py-2 text-sm font-medium text-white hover:bg-accent-hover"
          >
            Sign In
          </button>
          <p class="mt-4 text-center text-xs text-text-secondary">{version}</p>
        </div>
      </form>
    </section>
  </div>
</div>
