<script lang="ts">
  export type SkeletonRounded = 'none' | 'sm' | 'md' | 'lg' | 'full';

  let {
    width = '100%',
    height = '1rem',
    rounded = 'md'
  }: {
    width?: string;
    height?: string;
    rounded?: SkeletonRounded;
  } = $props();

  const roundedClasses: Record<SkeletonRounded, string> = {
    none: 'rounded-none',
    sm: 'rounded-sm',
    md: 'rounded-md',
    lg: 'rounded-lg',
    full: 'rounded-full'
  };

  const roundedClass = $derived(roundedClasses[rounded]);
</script>

<!--
  A single placeholder bar. It carries no meaning on its own, so it is hidden
  from assistive technology and the surrounding variant owns the status label.

  The pulse is behind `motion-safe:` so a reduced-motion preference leaves the
  bar visible but still.
-->
<div
  class={`bg-elevated motion-safe:animate-pulse ${roundedClass}`}
  style={`width: ${width}; height: ${height};`}
  aria-hidden="true"
></div>
