<script lang="ts">
  import Skeleton from './Skeleton.svelte';

  let {
    height = 320,
    label = 'Loading chart data'
  }: {
    height?: number;
    label?: string;
  } = $props();
</script>

<!-- Reserves the same box LineChart and BarChart occupy so nothing jumps on load. -->
<div
  class="w-full rounded-lg border border-border-default bg-surface p-4"
  style={`height: ${height}px`}
  role="status"
  aria-busy="true"
>
  <span class="sr-only">{label}</span>
  <Skeleton width="100%" height="100%" rounded="md" />
</div>
