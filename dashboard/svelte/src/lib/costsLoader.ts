/**
 * Costs page load orchestration.
 *
 * The page can start a new load before the previous one has answered — clicking
 * 7d then 30d, or retrying while a request is still open. Every load claims a
 * sequence number, and a load that is no longer the newest reports `stale` so its
 * response is dropped instead of overwriting the range the user actually picked.
 *
 * Kept out of the component so the ordering rules can be tested directly.
 */

import { costsQuery, resolveRange, seriesQuery, type DateRangeSelection, type ResolvedRange } from './dateRange.ts';
import type { CostBreakdown, CostsResponse } from './types.ts';

/** CostsFetcher issues one `/costs` request for an already-built query string. */
export type CostsFetcher = (query: string) => Promise<CostsResponse>;

/** LoadedCosts is a completed load that is still the newest one. */
export interface LoadedCosts {
  status: 'loaded';
  range: ResolvedRange;
  totalUSD: number;
  rows: CostBreakdown[];
  seriesRows: CostBreakdown[];
}

/** InvalidRange is a selection that failed validation before any request went out. */
export interface InvalidRange {
  status: 'invalid';
  error: string;
}

/** StaleLoad is a load that a newer selection has superseded. */
export interface StaleLoad {
  status: 'stale';
}

/** FailedLoad is a request failure that is still the newest load. */
export interface FailedLoad {
  status: 'failed';
  range: ResolvedRange;
  error: string;
}

/** CostsLoadOutcome is the result the page applies to its state. */
export type CostsLoadOutcome = LoadedCosts | InvalidRange | StaleLoad | FailedLoad;

/** CostsLoader loads one selection at a time, newest wins. */
export interface CostsLoader {
  load(selection: DateRangeSelection): Promise<CostsLoadOutcome>;
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : 'Failed to load costs.';
}

/**
 * createCostsLoader builds a loader over `fetcher`. `now` is injected so the
 * range a load resolves is deterministic under test.
 */
export function createCostsLoader(fetcher: CostsFetcher, now: () => Date): CostsLoader {
  let latest = 0;

  return {
    async load(selection: DateRangeSelection): Promise<CostsLoadOutcome> {
      // Claim the newest slot before validating, so an invalid selection also
      // supersedes whatever is in flight: the user has moved on from it.
      latest += 1;
      const sequence = latest;
      const isCurrent = () => sequence === latest;

      const resolution = resolveRange(selection, now());
      if (!resolution.ok) {
        return { status: 'invalid', error: resolution.error };
      }

      const range = resolution.range;
      try {
        const [totals, timeSeries] = await Promise.all([
          fetcher(costsQuery(range)),
          fetcher(seriesQuery(range))
        ]);
        if (!isCurrent()) {
          return { status: 'stale' };
        }
        return {
          status: 'loaded',
          range,
          totalUSD: totals.total_usd,
          rows: totals.breakdown,
          seriesRows: timeSeries.breakdown
        };
      } catch (err) {
        if (!isCurrent()) {
          return { status: 'stale' };
        }
        return { status: 'failed', range, error: errorMessage(err) };
      }
    }
  };
}
