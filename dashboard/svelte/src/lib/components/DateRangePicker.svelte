<script lang="ts">
  import type { DateRangePreset, DateRangeSelection } from '$lib/dateRange';

  const presets: Array<{ key: DateRangePreset; label: string }> = [
    { key: 'today', label: 'Today' },
    { key: '7d', label: '7d' },
    { key: '30d', label: '30d' },
    { key: 'custom', label: 'Custom' }
  ];

  let {
    selection,
    error = null,
    onChange,
    onDraft
  }: {
    selection: DateRangeSelection;
    error?: string | null;
    // onChange applies a selection and reloads. onDraft only records typing, so a
    // half-entered custom range never triggers a query.
    onChange?: (next: DateRangeSelection) => void;
    onDraft?: (next: DateRangeSelection) => void;
  } = $props();

  const invalid = $derived(selection.preset === 'custom' && error !== null);

  function selectPreset(preset: DateRangePreset): void {
    onChange?.({ ...selection, preset });
  }

  function draftStart(event: Event): void {
    const input = event.currentTarget as HTMLInputElement;
    onDraft?.({ ...selection, preset: 'custom', customStart: input.value });
  }

  function draftEnd(event: Event): void {
    const input = event.currentTarget as HTMLInputElement;
    onDraft?.({ ...selection, preset: 'custom', customEnd: input.value });
  }

  function applyCustom(event: SubmitEvent): void {
    event.preventDefault();
    onChange?.({ ...selection, preset: 'custom' });
  }
</script>

<div class="space-y-2">
  <div class="inline-flex items-center gap-2 rounded-lg border border-border-default bg-surface p-1">
    {#each presets as preset (preset.key)}
      <button
        type="button"
        aria-pressed={selection.preset === preset.key}
        class={`rounded-md px-3 py-1.5 text-xs font-medium transition-colors ${
          selection.preset === preset.key
            ? 'bg-accent text-white'
            : 'text-text-secondary hover:bg-elevated hover:text-text-primary'
        }`}
        onclick={() => selectPreset(preset.key)}
      >
        {preset.label}
      </button>
    {/each}
  </div>

  {#if selection.preset === 'custom'}
    <form
      class="flex flex-col gap-2 rounded-lg border border-border-default bg-surface p-3 sm:flex-row sm:items-end"
      onsubmit={applyCustom}
    >
      <div class="flex flex-col gap-1">
        <label class="text-[11px] font-medium uppercase tracking-wide text-text-muted" for="cost-range-start">
          Start
        </label>
        <input
          id="cost-range-start"
          type="date"
          required
          aria-invalid={invalid}
          aria-describedby={invalid ? 'cost-range-error' : undefined}
          class="rounded-md border border-border-default bg-elevated px-2 py-1.5 text-xs text-text-primary"
          value={selection.customStart}
          oninput={draftStart}
        />
      </div>
      <div class="flex flex-col gap-1">
        <label class="text-[11px] font-medium uppercase tracking-wide text-text-muted" for="cost-range-end">
          End
        </label>
        <input
          id="cost-range-end"
          type="date"
          required
          aria-invalid={invalid}
          aria-describedby={invalid ? 'cost-range-error' : undefined}
          class="rounded-md border border-border-default bg-elevated px-2 py-1.5 text-xs text-text-primary"
          value={selection.customEnd}
          oninput={draftEnd}
        />
      </div>
      <button
        type="submit"
        class="rounded-md bg-accent px-3 py-1.5 text-xs font-medium text-white hover:bg-accent-hover"
      >
        Apply
      </button>
    </form>

    {#if error}
      <p id="cost-range-error" role="alert" class="text-xs text-danger">{error}</p>
    {/if}
  {/if}
</div>
