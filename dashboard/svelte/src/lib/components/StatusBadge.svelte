<script lang="ts">
  /**
   * `pending` is for a value that has not been measured yet. It is deliberately
   * neutral: a badge that has not read anything must not colour itself as a
   * verdict.
   */
  export type BadgeStatus =
    | 'active'
    | 'near_limit'
    | 'killed'
    | 'success'
    | 'error'
    | 'warning'
    | 'pending';

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
    },
    pending: {
      label: 'Checking',
      classes: 'bg-elevated text-text-secondary border-border-default'
    }
  };
</script>

<span
  class={`inline-flex items-center rounded-full border px-2.5 py-1 text-xs font-medium ${statusStyles[status].classes}`}
>
  {statusStyles[status].label}
</span>
