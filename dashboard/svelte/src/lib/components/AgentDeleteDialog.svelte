<script lang="ts">
  import { confirmsAgentName } from '$lib/agentDelete';

  let {
    open,
    agentName,
    busy = false,
    errorMessage = null,
    onClose,
    onConfirm
  }: {
    open: boolean;
    agentName: string | null;
    busy?: boolean;
    errorMessage?: string | null;
    onClose?: () => void;
    onConfirm?: (agentName: string) => Promise<void>;
  } = $props();

  let typedName = $state('');
  let dialog = $state<HTMLElement | null>(null);

  const canConfirm = $derived(!busy && confirmsAgentName(typedName, agentName));

  // Typing the exact name is the deliberate step: a stray click on the row
  // action can never remove an agent by itself. agentName is read here on
  // purpose, so a confirmation typed for one agent is never carried over to
  // another. Focus moves in on open and back to the opener on close, which is
  // what makes aria-modal honest for keyboard and screen reader users.
  $effect(() => {
    if (!open) {
      return;
    }
    void agentName;
    typedName = '';

    const opener = document.activeElement as HTMLElement | null;
    dialog?.focus();
    return () => opener?.focus?.();
  });

  function close(): void {
    if (!busy) {
      onClose?.();
    }
  }

  // Focusable children in DOM order. Disabled controls are skipped, so the
  // cycle shrinks to whatever is actually usable in the current state.
  function focusableItems(): HTMLElement[] {
    if (!dialog) {
      return [];
    }
    return Array.from(dialog.querySelectorAll<HTMLElement>('input:not([disabled]), button:not([disabled])'));
  }

  function trapTab(event: KeyboardEvent): void {
    const items = focusableItems();
    if (items.length === 0) {
      // Nothing usable right now (a delete in flight disables every control).
      // Keep focus on the dialog rather than letting Tab walk the page behind.
      event.preventDefault();
      dialog?.focus();
      return;
    }

    const first = items[0];
    const last = items[items.length - 1];
    const active = document.activeElement;

    if (event.shiftKey && (active === first || active === dialog)) {
      event.preventDefault();
      last.focus();
      return;
    }
    if (!event.shiftKey && (active === last || active === dialog)) {
      event.preventDefault();
      first.focus();
    }
  }

  function onKeydown(event: KeyboardEvent): void {
    if (event.key === 'Escape') {
      event.preventDefault();
      close();
      return;
    }
    if (event.key === 'Tab') {
      trapTab(event);
    }
  }

  async function submit(): Promise<void> {
    if (!canConfirm || !agentName || !onConfirm) {
      return;
    }
    await onConfirm(agentName);
  }
</script>

{#if open && agentName !== null}
  <button type="button" aria-label="Cancel deleting agent" class="fixed inset-0 z-40 bg-black/60" disabled={busy} onclick={close}></button>
  <div
    bind:this={dialog}
    role="dialog"
    aria-modal="true"
    aria-labelledby="agent-delete-title"
    aria-describedby="agent-delete-description"
    tabindex="-1"
    class="fixed left-1/2 top-1/2 z-50 w-[calc(100%-2rem)] max-w-lg -translate-x-1/2 -translate-y-1/2 rounded-lg border border-border-default bg-surface p-6 shadow-2xl focus:outline-none focus:ring-2 focus:ring-accent"
    onkeydown={onKeydown}
  >
    <h2 id="agent-delete-title" class="text-xl font-semibold text-text-primary">Delete agent “{agentName}”?</h2>

    <div id="agent-delete-description" class="mt-3 space-y-3 text-sm text-text-secondary">
      <p>This removes everything Oberwatch stores for this agent:</p>
      <ul class="list-disc space-y-1 pl-5">
        <li>the agent record and its budget state (spend, period, kill status)</li>
        <li>all of its cost records, so its spend history and charts disappear</li>
        <li>all of its alerts</li>
      </ul>
      <p>
        Task budgets, other agents, pricing, the global budget, and settings are not changed. Tasks that last
        ran under this agent keep their totals but no longer point at it.
      </p>
      <p>
        The agent is not blocked. The next proxied request that identifies as “{agentName}” recreates it with
        the default budget policy and a spend of $0.
      </p>
    </div>

    <form
      class="mt-5 space-y-4"
      onsubmit={(event) => {
        event.preventDefault();
        void submit();
      }}
    >
      <label class="block space-y-2">
        <span class="text-xs font-medium uppercase tracking-wide text-text-secondary">
          Type the agent name to confirm
        </span>
        <input
          class="w-full rounded-md border border-border-default bg-elevated px-3 py-2 font-mono text-sm text-text-primary focus:border-accent focus:outline-none"
          bind:value={typedName}
          placeholder={agentName}
          autocomplete="off"
          spellcheck="false"
          disabled={busy}
          aria-describedby="agent-delete-hint"
        />
        <span id="agent-delete-hint" class="block text-xs text-text-muted">Deletion is enabled once the name matches exactly.</span>
      </label>

      {#if errorMessage}
        <div role="alert" class="rounded-md border border-danger/40 bg-danger/10 px-3 py-2 text-sm text-danger">{errorMessage}</div>
      {/if}

      <div class="flex items-center justify-end gap-3">
        <button type="button" class="rounded-md border border-border-default bg-elevated px-4 py-2 text-sm text-text-primary" disabled={busy} onclick={close}>
          Cancel
        </button>
        <button
          type="submit"
          class="rounded-md bg-danger px-4 py-2 text-sm font-medium text-white disabled:cursor-not-allowed disabled:opacity-60"
          disabled={!canConfirm}
          aria-busy={busy}
        >
          {busy ? 'Deleting…' : 'Delete agent'}
        </button>
      </div>
    </form>
  </div>
{/if}
