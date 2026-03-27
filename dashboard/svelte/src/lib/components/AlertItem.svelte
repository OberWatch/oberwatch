<script lang="ts">
  import type { Alert } from '$lib/types';

  let { alert }: { alert: Alert } = $props();

  function severityColor(severity: string): string {
    switch (severity) {
      case 'success':
      case 'info':
        return 'bg-success';
      case 'warning':
        return 'bg-warning';
      case 'error':
      case 'critical':
      default:
        return 'bg-danger';
    }
  }

  function relativeTime(timestamp?: string): string {
    if (!timestamp) {
      return 'just now';
    }

    const target = new Date(timestamp).getTime();
    if (Number.isNaN(target)) {
      return 'unknown time';
    }

    const deltaSeconds = Math.max(0, Math.floor((Date.now() - target) / 1000));
    if (deltaSeconds < 60) return `${deltaSeconds}s ago`;
    if (deltaSeconds < 3600) return `${Math.floor(deltaSeconds / 60)}m ago`;
    if (deltaSeconds < 86400) return `${Math.floor(deltaSeconds / 3600)}h ago`;
    return `${Math.floor(deltaSeconds / 86400)}d ago`;
  }

  const dotTone = $derived(severityColor(alert.severity));
  const timestampText = $derived(relativeTime(alert.timestamp));
</script>

<article class="flex items-start justify-between gap-3 rounded-md border border-border-default bg-surface p-3">
  <div class="flex min-w-0 items-start gap-2">
    <span class={`mt-1 h-2.5 w-2.5 shrink-0 rounded-full ${dotTone}`} aria-hidden="true"></span>
    <div class="min-w-0">
      <p class="truncate text-sm text-text-primary">{alert.message}</p>
      <p class="mt-1 text-xs font-medium text-text-secondary">{alert.agent}</p>
    </div>
  </div>
  <time class="shrink-0 text-xs text-text-muted" datetime={alert.timestamp}>{timestampText}</time>
</article>
