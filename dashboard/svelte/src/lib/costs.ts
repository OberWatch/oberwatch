/**
 * Cost chart shaping.
 *
 * These helpers turn `/costs` breakdown rows into chart-ready axes. They are kept
 * free of Svelte and Chart.js so the aggregation rules can be tested directly.
 */

import type { BucketGranularity } from './dateRange.ts';
import type { CostBreakdown } from './types.ts';

/** CostBreakdownRow is one row of a `/costs` breakdown response. */
export type CostBreakdownRow = CostBreakdown;

/** CategoryTotals is a single-dimension bar axis ranked by cost. */
export interface CategoryTotals {
  labels: string[];
  values: number[];
}

/** SeriesDataset is one agent's contribution across every bucket in a series. */
export interface SeriesDataset {
  label: string;
  data: number[];
}

/**
 * StackedSeries is a dense agent-by-bucket series. `bucketKeys` keeps the raw
 * API bucket values for assertions and tooltips; `labels` is what the axis shows.
 * Every dataset holds exactly one value per bucket, which is what makes the
 * stacked axes add up.
 */
export interface StackedSeries {
  bucketKeys: string[];
  labels: string[];
  datasets: SeriesDataset[];
}

const UNKNOWN_KEY = 'unknown';

const MONTH_ABBREVIATIONS = [
  'Jan',
  'Feb',
  'Mar',
  'Apr',
  'May',
  'Jun',
  'Jul',
  'Aug',
  'Sep',
  'Oct',
  'Nov',
  'Dec'
];

const DAY_BUCKET_PATTERN = /^(\d{4})-(\d{2})-(\d{2})$/;

function dimensionKey(raw: string | undefined): string {
  const trimmed = (raw ?? '').trim();
  return trimmed === '' ? UNKNOWN_KEY : trimmed;
}

/** rankByCostDescending orders entries by cost, falling back to the key for ties. */
function rankByCostDescending(totals: Map<string, number>): CategoryTotals {
  const entries = [...totals.entries()].sort((left, right) => {
    if (right[1] !== left[1]) {
      return right[1] - left[1];
    }
    return left[0].localeCompare(right[0]);
  });

  return {
    labels: entries.map(([label]) => label),
    values: entries.map(([, value]) => value)
  };
}

function totalsByDimension(
  rows: CostBreakdownRow[],
  pick: (row: CostBreakdownRow) => string | undefined
): CategoryTotals {
  const totals = new Map<string, number>();
  for (const row of rows) {
    const key = dimensionKey(pick(row));
    totals.set(key, (totals.get(key) ?? 0) + row.cost_usd);
  }
  return rankByCostDescending(totals);
}

/** totalsByAgent aggregates cost per agent for the cost-by-agent bar chart. */
export function totalsByAgent(rows: CostBreakdownRow[]): CategoryTotals {
  return totalsByDimension(rows, (row) => row.agent);
}

/** totalsByModel aggregates cost per model for the cost-by-model bar chart. */
export function totalsByModel(rows: CostBreakdownRow[]): CategoryTotals {
  return totalsByDimension(rows, (row) => row.model);
}

function plural(count: number, noun: string): string {
  return `${count} ${noun}${count === 1 ? '' : 's'}`;
}

/**
 * describeStackedSeries writes the chart's accessible label. A canvas is opaque
 * to a screen reader, so this states what the chart plots and how much of it
 * there is; the page pairs it with a table of the same numbers.
 */
export function describeStackedSeries(series: StackedSeries, rangeLabel: string): string {
  if (series.datasets.length === 0 || series.bucketKeys.length === 0) {
    return `No cost over time data for ${rangeLabel}.`;
  }

  return (
    `Cost over time for ${rangeLabel}, stacked by agent: ` +
    `${plural(series.datasets.length, 'agent')} across ` +
    `${plural(series.bucketKeys.length, 'time bucket')}.`
  );
}

/** localDayKey names the local calendar day that contains `instant`. */
function localDayKey(instant: Date): string {
  const year = String(instant.getFullYear()).padStart(4, '0');
  const month = String(instant.getMonth() + 1).padStart(2, '0');
  const day = String(instant.getDate()).padStart(2, '0');
  return `${year}-${month}-${day}`;
}

/**
 * normalizeBucketKey maps one API bucket onto the key the chart groups by.
 *
 * The series is always fetched as absolute hourly instants. Folding those
 * instants into days here — rather than truncating a UTC date in SQL — is what
 * keeps a day bucket equal to a day on the viewer's calendar. A local day in a
 * negative UTC offset spans two UTC dates, and daylight-saving days are 23 or 25
 * hours long; reading each instant through the local calendar handles both.
 *
 * Values that are already `YYYY-MM-DD` are passed through, so day-grouped rows
 * from the API still render. Unparseable buckets return null and are dropped.
 */
function normalizeBucketKey(rawBucket: string, bucket: BucketGranularity): string | null {
  if (DAY_BUCKET_PATTERN.test(rawBucket)) {
    return rawBucket;
  }

  const parsed = new Date(rawBucket);
  if (Number.isNaN(parsed.getTime())) {
    return null;
  }

  return bucket === 'day' ? localDayKey(parsed) : rawBucket;
}

/**
 * bucketSortValue orders bucket keys chronologically. Day buckets are read as
 * local calendar dates so a negative UTC offset cannot shift them a day earlier.
 */
function bucketSortValue(bucketKey: string): number {
  const dayMatch = DAY_BUCKET_PATTERN.exec(bucketKey);
  if (dayMatch) {
    return new Date(Number(dayMatch[1]), Number(dayMatch[2]) - 1, Number(dayMatch[3])).getTime();
  }
  const parsed = new Date(bucketKey).getTime();
  return Number.isNaN(parsed) ? 0 : parsed;
}

/** formatBucketLabel renders an axis tick for one bucket key. */
function formatBucketLabel(bucketKey: string, bucket: BucketGranularity): string {
  const dayMatch = DAY_BUCKET_PATTERN.exec(bucketKey);
  if (dayMatch) {
    const month = MONTH_ABBREVIATIONS[Number(dayMatch[2]) - 1] ?? dayMatch[2];
    return `${month} ${dayMatch[3]}`;
  }

  const parsed = new Date(bucketKey);
  if (Number.isNaN(parsed.getTime())) {
    return bucketKey;
  }
  if (bucket === 'day') {
    return `${MONTH_ABBREVIATIONS[parsed.getMonth()]} ${String(parsed.getDate()).padStart(2, '0')}`;
  }
  return `${String(parsed.getHours()).padStart(2, '0')}:${String(parsed.getMinutes()).padStart(2, '0')}`;
}

/**
 * buildStackedSeries turns agent-by-bucket rows (`group_by=agent_hour` or
 * `agent_day`) into a dense stacked series: one dataset per agent, one value per
 * bucket, zero-filled where an agent spent nothing. Rows without a bucket cannot
 * be placed on a time axis and are dropped.
 */
export function buildStackedSeries(
  rows: CostBreakdownRow[],
  bucket: BucketGranularity
): StackedSeries {
  const bucketKeys = new Set<string>();
  const costByAgentBucket = new Map<string, Map<string, number>>();
  const totalByAgent = new Map<string, number>();

  for (const row of rows) {
    const rawBucket = (row.bucket ?? '').trim();
    if (rawBucket === '') {
      continue;
    }
    const bucketKey = normalizeBucketKey(rawBucket, bucket);
    if (bucketKey === null) {
      continue;
    }
    bucketKeys.add(bucketKey);

    const agent = dimensionKey(row.agent);
    let agentBuckets = costByAgentBucket.get(agent);
    if (!agentBuckets) {
      agentBuckets = new Map<string, number>();
      costByAgentBucket.set(agent, agentBuckets);
    }
    agentBuckets.set(bucketKey, (agentBuckets.get(bucketKey) ?? 0) + row.cost_usd);
    totalByAgent.set(agent, (totalByAgent.get(agent) ?? 0) + row.cost_usd);
  }

  const sortedBucketKeys = [...bucketKeys].sort(
    (left, right) => bucketSortValue(left) - bucketSortValue(right)
  );
  const rankedAgents = rankByCostDescending(totalByAgent).labels;

  return {
    bucketKeys: sortedBucketKeys,
    labels: sortedBucketKeys.map((bucketKey) => formatBucketLabel(bucketKey, bucket)),
    datasets: rankedAgents.map((agent) => {
      const agentBuckets = costByAgentBucket.get(agent);
      return {
        label: agent,
        data: sortedBucketKeys.map((bucketKey) => agentBuckets?.get(bucketKey) ?? 0)
      };
    })
  };
}
