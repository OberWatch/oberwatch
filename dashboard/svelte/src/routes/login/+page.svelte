<script lang="ts">
  import { fetchJSON } from '$lib/api';

  let username = $state('admin');
  let password = $state('');
  let errorMessage = $state<string | null>(null);

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
</script>

<div class="flex min-h-screen items-center justify-center bg-base px-6">
  <section class="w-full max-w-md rounded-2xl border border-border-default bg-surface p-8 shadow-lg shadow-black/20">
    <div class="space-y-2">
      <p class="text-xs uppercase tracking-[0.3em] text-accent">Oberwatch</p>
      <h1 class="text-3xl font-semibold text-text-primary">Sign in</h1>
      <p class="text-sm text-text-secondary">Use your admin account to access the dashboard.</p>
    </div>

    <div class="mt-6 space-y-4">
      <label class="block space-y-1">
        <span class="text-xs uppercase tracking-wide text-text-secondary">Username</span>
        <input
          bind:value={username}
          type="text"
          class="w-full rounded-md border border-border-default bg-elevated px-3 py-2 text-sm text-text-primary outline-none"
        />
      </label>

      <label class="block space-y-1">
        <span class="text-xs uppercase tracking-wide text-text-secondary">Password</span>
        <input
          bind:value={password}
          type="password"
          class="w-full rounded-md border border-border-default bg-elevated px-3 py-2 text-sm text-text-primary outline-none"
        />
      </label>

      {#if errorMessage}
        <p class="text-sm text-danger">{errorMessage}</p>
      {/if}

      <button
        type="button"
        class="w-full rounded-md bg-accent px-4 py-2 text-sm font-medium text-white hover:bg-accent-hover"
        onclick={signIn}
      >
        Sign In
      </button>
    </div>
  </section>
</div>
