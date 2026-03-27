<script lang="ts">
  export type SortDirection = 'asc' | 'desc';
  export type ColumnDef = {
    key: string;
    label: string;
    sortable?: boolean;
  };
  export type RowData = Record<string, string | number | boolean | null | undefined>;

  let {
    columns,
    rows,
    onSort
  }: {
    columns: ColumnDef[];
    rows: RowData[];
    onSort?: (key: string, direction: SortDirection) => void;
  } = $props();

  let sortKey = $state<string | null>(null);
  let sortDirection = $state<SortDirection>('asc');

  function asComparable(value: RowData[string]): string | number {
    if (typeof value === 'number') {
      return value;
    }
    if (typeof value === 'boolean') {
      return value ? 1 : 0;
    }
    if (value === null || value === undefined) {
      return '';
    }
    return value.toLowerCase();
  }

  function compareRows(a: RowData, b: RowData, key: string): number {
    const left = asComparable(a[key]);
    const right = asComparable(b[key]);

    if (left < right) return -1;
    if (left > right) return 1;
    return 0;
  }

  function applySort(column: ColumnDef): void {
    if (!column.sortable) {
      return;
    }

    if (sortKey === column.key) {
      sortDirection = sortDirection === 'asc' ? 'desc' : 'asc';
    } else {
      sortKey = column.key;
      sortDirection = 'asc';
    }

    onSort?.(sortKey, sortDirection);
  }

  const sortedRows = $derived.by(() => {
    if (!sortKey) {
      return rows;
    }

    const key = sortKey;

    return [...rows].sort((a, b) => {
      const base = compareRows(a, b, key);
      return sortDirection === 'asc' ? base : -base;
    });
  });
</script>

<div class="overflow-x-auto rounded-lg border border-border-default bg-surface">
  <table class="min-w-full divide-y divide-border-default">
    <thead class="bg-elevated/30">
      <tr>
        {#each columns as column}
          <th
            scope="col"
            class="px-4 py-3 text-left text-xs font-medium uppercase tracking-wide text-text-secondary"
          >
            {#if column.sortable}
              <button
                type="button"
                class="inline-flex items-center gap-1 transition-colors hover:text-text-primary"
                onclick={() => applySort(column)}
              >
                <span>{column.label}</span>
                {#if sortKey === column.key}
                  <span aria-hidden="true">{sortDirection === 'asc' ? '↑' : '↓'}</span>
                {:else}
                  <span aria-hidden="true" class="text-text-muted">↕</span>
                {/if}
              </button>
            {:else}
              {column.label}
            {/if}
          </th>
        {/each}
      </tr>
    </thead>
    <tbody class="divide-y divide-border-default">
      {#if sortedRows.length === 0}
        <tr>
          <td colspan={columns.length} class="px-4 py-6 text-center text-sm text-text-muted">
            No data available
          </td>
        </tr>
      {:else}
        {#each sortedRows as row}
          <tr class="bg-surface transition-colors hover:bg-elevated">
            {#each columns as column}
              <td class="px-4 py-3 text-sm text-text-primary">
                {#if row[column.key] === null || row[column.key] === undefined}
                  <span class="text-text-muted">-</span>
                {:else}
                  {String(row[column.key])}
                {/if}
              </td>
            {/each}
          </tr>
        {/each}
      {/if}
    </tbody>
  </table>
</div>
