<script lang="ts">
  import { fetchJSON } from '$lib/api';

  let step = $state<1 | 2>(1);
  let username = $state('admin');
  let password = $state('');
  let confirmPassword = $state('');
  let errorMessage = $state<string | null>(null);

  const proxyURL = $derived(typeof window === 'undefined' ? '' : window.location.origin);

  async function createAccount(): Promise<void> {
    errorMessage = null;

    try {
      await fetchJSON('/setup', {
        method: 'POST',
        body: JSON.stringify({
          username,
          password,
          confirm_password: confirmPassword
        })
      });
      step = 2;
    } catch (error) {
      errorMessage = error instanceof Error ? error.message : 'Failed to create account.';
    }
  }

  function goToDashboard(): void {
    window.location.assign('/');
  }
</script>

<div class="flex min-h-screen items-center justify-center bg-base px-6">
  <section class="w-full max-w-2xl rounded-3xl border border-border-default bg-surface p-8 shadow-2xl shadow-black/20">
    {#if step === 1}
      <div class="space-y-2">
        <p class="text-xs uppercase tracking-[0.3em] text-accent">First Run</p>
        <h1 class="text-3xl font-semibold text-text-primary">Welcome to Oberwatch</h1>
        <p class="text-sm text-text-secondary">Create the single admin account for this instance.</p>
      </div>

      <div class="mt-6 grid gap-4 md:grid-cols-3">
        <label class="space-y-1 md:col-span-1">
          <span class="text-xs uppercase tracking-wide text-text-secondary">Username</span>
          <input
            bind:value={username}
            type="text"
            class="w-full rounded-md border border-border-default bg-elevated px-3 py-2 text-sm text-text-primary outline-none"
          />
        </label>
        <label class="space-y-1 md:col-span-1">
          <span class="text-xs uppercase tracking-wide text-text-secondary">Password</span>
          <input
            bind:value={password}
            type="password"
            class="w-full rounded-md border border-border-default bg-elevated px-3 py-2 text-sm text-text-primary outline-none"
          />
        </label>
        <label class="space-y-1 md:col-span-1">
          <span class="text-xs uppercase tracking-wide text-text-secondary">Confirm password</span>
          <input
            bind:value={confirmPassword}
            type="password"
            class="w-full rounded-md border border-border-default bg-elevated px-3 py-2 text-sm text-text-primary outline-none"
          />
        </label>
      </div>

      {#if errorMessage}
        <p class="mt-4 text-sm text-danger">{errorMessage}</p>
      {/if}

      <button
        type="button"
        class="mt-6 rounded-md bg-accent px-5 py-2 text-sm font-medium text-white hover:bg-accent-hover"
        onclick={createAccount}
      >
        Create Account
      </button>
    {:else}
      <div class="space-y-2">
        <p class="text-xs uppercase tracking-[0.3em] text-success">Setup Complete</p>
        <h1 class="text-3xl font-semibold text-text-primary">You're ready!</h1>
        <p class="text-sm text-text-secondary">
          Point your agents at this URL instead of `api.openai.com`.
        </p>
      </div>

      <div class="mt-6 rounded-xl border border-border-default bg-elevated p-4">
        <p class="text-xs uppercase tracking-wide text-text-secondary">Proxy URL</p>
        <p class="mt-2 font-mono text-sm text-text-primary">{proxyURL}</p>
      </div>

      <div class="mt-4 space-y-2 text-sm text-text-secondary">
        <p>Point your agents at this URL instead of `api.openai.com`.</p>
        <p>Open the dashboard to start monitoring spend, traces, and governance.</p>
      </div>

      <button
        type="button"
        class="mt-6 rounded-md bg-accent px-5 py-2 text-sm font-medium text-white hover:bg-accent-hover"
        onclick={goToDashboard}
      >
        Go to Dashboard
      </button>
    {/if}
  </section>
</div>
