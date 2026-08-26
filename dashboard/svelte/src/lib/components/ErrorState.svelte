<script lang="ts">
  let {
    message,
    onRetry,
    retryLabel = 'Retry'
  }: {
    message: string;
    onRetry: () => void;
    retryLabel?: string;
  } = $props();
</script>

<!--
  The one API-failure state for the dashboard. Retry is the caller's own reload
  function, so the page keeps whatever range, filter or selection is active.
-->
<div class="rounded-lg border border-danger/40 bg-danger/10 p-4" role="alert">
  <p class="text-sm text-danger">{message}</p>
  <button
    type="button"
    class="mt-3 rounded-md bg-accent px-3 py-1.5 text-xs font-medium text-white hover:bg-accent-hover"
    onclick={onRetry}
  >
    {retryLabel}
  </button>
</div>
