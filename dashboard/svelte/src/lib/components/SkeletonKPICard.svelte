<script lang="ts">
  import Skeleton from './Skeleton.svelte';

  let { label = 'Loading metric' }: { label?: string } = $props();
</script>

<article
  class="rounded-lg border border-border-default bg-surface p-4"
  role="status"
  aria-busy="true"
>
  <span class="sr-only">{label}</span>
  <Skeleton width="45%" height="0.875rem" rounded="sm" />
  <div class="mt-3">
    <Skeleton width="60%" height="1.875rem" rounded="md" />
  </div>
  <div class="mt-2">
    <Skeleton width="35%" height="0.75rem" rounded="sm" />
  </div>
</article>
