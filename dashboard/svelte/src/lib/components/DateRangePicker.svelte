<script lang="ts">
  export type DateRangePreset = 'today' | '7d' | '30d' | 'custom';

  const presets: Array<{ key: DateRangePreset; label: string }> = [
    { key: 'today', label: 'Today' },
    { key: '7d', label: '7d' },
    { key: '30d', label: '30d' },
    { key: 'custom', label: 'Custom' }
  ];

  let {
    selected,
    onChange
  }: {
    selected: DateRangePreset;
    onChange?: (next: DateRangePreset) => void;
  } = $props();
</script>

<div class="inline-flex items-center gap-2 rounded-lg border border-border-default bg-surface p-1">
  {#each presets as preset}
    <button
      type="button"
      class={`rounded-md px-3 py-1.5 text-xs font-medium transition-colors ${
        selected === preset.key
          ? 'bg-accent text-white'
          : 'text-text-secondary hover:bg-elevated hover:text-text-primary'
      }`}
      onclick={() => onChange?.(preset.key)}
    >
      {preset.label}
    </button>
  {/each}
</div>
