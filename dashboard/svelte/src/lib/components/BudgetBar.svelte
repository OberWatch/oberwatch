<script lang="ts">
  import { formatUSD } from '$lib/currency';

  let {
    percentage,
    spentUSD,
    limitUSD
  }: {
    percentage: number;
    spentUSD: number;
    limitUSD: number;
  } = $props();

  const clampedPercent = $derived(Math.max(0, Math.min(100, percentage)));
  const toneClass = $derived(
    clampedPercent < 50 ? 'bg-success' : clampedPercent < 80 ? 'bg-warning' : 'bg-danger'
  );

  const formattedSpent = $derived(formatUSD(spentUSD));

  const formattedLimit = $derived(formatUSD(limitUSD));
</script>

<div class="w-full">
  <div class="mb-2 flex items-center justify-between text-xs font-medium">
    <span class="text-text-secondary">{formattedSpent} / {formattedLimit}</span>
    <span class="text-text-primary">{clampedPercent.toFixed(1)}%</span>
  </div>
  <div class="h-2.5 overflow-hidden rounded-full bg-elevated">
    <div class={`h-full ${toneClass}`} style={`width: ${clampedPercent}%`}></div>
  </div>
</div>
