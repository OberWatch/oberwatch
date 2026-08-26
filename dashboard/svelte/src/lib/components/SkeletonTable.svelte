<script lang="ts">
  import Skeleton from './Skeleton.svelte';

  let {
    rows = 6,
    columns = 4,
    label = 'Loading table data'
  }: {
    rows?: number;
    columns?: number;
    label?: string;
  } = $props();
</script>

<div
  class="overflow-hidden rounded-lg border border-border-default bg-surface"
  role="status"
  aria-busy="true"
>
  <span class="sr-only">{label}</span>
  {#each Array.from({ length: rows }) as _, rowIndex (rowIndex)}
    <div class="flex items-center gap-4 border-b border-border-default px-4 py-4 last:border-b-0">
      {#each Array.from({ length: columns }) as _, columnIndex (columnIndex)}
        <div class="flex-1">
          <Skeleton width="100%" height="0.875rem" rounded="sm" />
        </div>
      {/each}
    </div>
  {/each}
</div>
