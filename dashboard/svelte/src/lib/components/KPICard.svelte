<script lang="ts">
  export type TrendDirection = 'up' | 'down';

  let {
    title,
    value,
    subtitle,
    trend,
    trendLabel
  }: {
    title: string;
    value: string | number;
    subtitle?: string;
    trend?: TrendDirection;
    trendLabel?: string;
  } = $props();

  const trendClass = $derived(
    trend === 'up' ? 'text-success' : trend === 'down' ? 'text-danger' : 'text-text-secondary'
  );
</script>

<article class="rounded-lg border border-border-default bg-surface p-4">
  <p class="text-sm font-medium text-text-secondary">{title}</p>
  <p class="mt-2 text-3xl font-bold text-text-primary">{value}</p>

  {#if subtitle}
    <p class="mt-1 text-xs font-medium text-text-muted">{subtitle}</p>
  {/if}

  {#if trend}
    <div class={`mt-3 inline-flex items-center gap-1 text-xs font-medium ${trendClass}`}>
      <span aria-hidden="true">{trend === 'up' ? '↑' : '↓'}</span>
      <span>{trendLabel ?? (trend === 'up' ? 'Increasing' : 'Decreasing')}</span>
    </div>
  {/if}
</article>
