import type { ProviderAvailability, ProviderStatus } from './types';

/**
 * How often the Settings page re-reads `/health` while it stays open. The
 * server refreshes its own snapshot at most once per TTL, so this only decides
 * how quickly a change is picked up; it never causes extra provider probes.
 */
export const PROVIDER_REFRESH_INTERVAL_MS = 15_000;

export type ProviderBadgeStatus = 'success' | 'warning' | 'error';

/** One refresh attempt: either the rows the API returned, or a failure. */
export type ProviderRefreshResult = { ok: true; rows: ProviderStatus[] } | { ok: false };

/** isProviderPending is true for a row that has never been probed. */
export function isProviderPending(row: ProviderStatus): boolean {
  return !row.observed_at;
}

export function providerBadgeStatus(status: ProviderAvailability): ProviderBadgeStatus {
  if (status === 'operational') return 'success';
  if (status === 'degraded') return 'warning';
  return 'error';
}

/**
 * providerStatusLabel is the short line under a provider card. A row that has
 * not been probed yet says so instead of reporting the placeholder status as
 * a result.
 */
export function providerStatusLabel(row: ProviderStatus): string {
  if (isProviderPending(row)) return 'Checking';
  switch (row.status) {
    case 'operational':
      return 'Operational';
    case 'degraded':
      return 'Degraded';
    case 'outage':
      return 'Outage';
    case 'unreachable':
      return 'Unreachable';
    default:
      return 'Status unavailable';
  }
}

/**
 * nextProviderRows decides what the cards show after a refresh attempt. A
 * successful read replaces the rows, whatever their statuses, because the
 * server already keeps the card set fixed. A failed read keeps the rows that
 * were on screen, so a transient request failure never blanks the grid.
 */
export function nextProviderRows(
  previous: ProviderStatus[],
  result: ProviderRefreshResult
): ProviderStatus[] {
  if (!result.ok) {
    return previous;
  }
  return result.rows;
}

/**
 * providerObservedAt is the most recent probe time across the rows, or null
 * when nothing has been probed yet or a timestamp cannot be parsed.
 */
export function providerObservedAt(rows: ProviderStatus[]): Date | null {
  let latest: Date | null = null;
  for (const row of rows) {
    if (!row.observed_at) continue;
    const parsed = new Date(row.observed_at);
    if (Number.isNaN(parsed.getTime())) continue;
    if (latest === null || parsed.getTime() > latest.getTime()) {
      latest = parsed;
    }
  }
  return latest;
}
