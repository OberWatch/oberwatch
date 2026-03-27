<script lang="ts">
  export type BadgeStatus = 'active' | 'near_limit' | 'killed' | 'success' | 'error' | 'warning';

  let { status }: { status: BadgeStatus } = $props();

  const statusStyles: Record<BadgeStatus, { label: string; classes: string }> = {
    active: {
      label: 'Active',
      classes: 'bg-success/15 text-success border-success/30'
    },
    near_limit: {
      label: 'Near limit',
      classes: 'bg-warning/15 text-warning border-warning/30'
    },
    killed: {
      label: 'Killed',
      classes: 'bg-danger/15 text-danger border-danger/30'
    },
    success: {
      label: 'Success',
      classes: 'bg-success/15 text-success border-success/30'
    },
    error: {
      label: 'Error',
      classes: 'bg-danger/15 text-danger border-danger/30'
    },
    warning: {
      label: 'Warning',
      classes: 'bg-warning/15 text-warning border-warning/30'
    }
  };
</script>

<span
  class={`inline-flex items-center rounded-full border px-2.5 py-1 text-xs font-medium ${statusStyles[status].classes}`}
>
  {statusStyles[status].label}
</span>
