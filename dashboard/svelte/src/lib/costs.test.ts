// Pin the zone before any Date use so bucket-label assertions are deterministic.
process.env.TZ = 'America/New_York';

import { test } from 'node:test';
import assert from 'node:assert/strict';

import {
  buildStackedSeries,
  describeStackedSeries,
  totalsByAgent,
  totalsByModel,
  type CostBreakdownRow
} from './costs.ts';

function row(overrides: Partial<CostBreakdownRow> = {}): CostBreakdownRow {
  return {
    agent: 'agent-a',
    model: 'gpt-4o',
    bucket: '2026-03-26T10:00:00Z',
    requests: 1,
    input_tokens: 10,
    output_tokens: 5,
    cost_usd: 0.01,
    ...overrides
  };
}

test('totalsByAgent and totalsByModel sum and rank one dimension', () => {
  const cases: Array<{
    name: string;
    rows: CostBreakdownRow[];
    wantAgent: { labels: string[]; values: number[] };
    wantModel: { labels: string[]; values: number[] };
  }> = [
    {
      name: 'sums duplicates and ranks by cost descending',
      rows: [
        row({ agent: 'agent-a', model: 'gpt-4o', cost_usd: 1 }),
        row({ agent: 'agent-a', model: 'gpt-4o-mini', cost_usd: 2 }),
        row({ agent: 'agent-b', model: 'gpt-4o', cost_usd: 10 })
      ],
      wantAgent: { labels: ['agent-b', 'agent-a'], values: [10, 3] },
      wantModel: { labels: ['gpt-4o', 'gpt-4o-mini'], values: [11, 2] }
    },
    {
      name: 'blank dimensions collapse into unknown',
      rows: [row({ agent: '', model: '', cost_usd: 0.5 })],
      wantAgent: { labels: ['unknown'], values: [0.5] },
      wantModel: { labels: ['unknown'], values: [0.5] }
    },
    {
      name: 'equal costs fall back to a stable name order',
      rows: [
        row({ agent: 'zeta', model: 'zeta-model', cost_usd: 1 }),
        row({ agent: 'alpha', model: 'alpha-model', cost_usd: 1 })
      ],
      wantAgent: { labels: ['alpha', 'zeta'], values: [1, 1] },
      wantModel: { labels: ['alpha-model', 'zeta-model'], values: [1, 1] }
    },
    {
      name: 'no rows produce empty axes',
      rows: [],
      wantAgent: { labels: [], values: [] },
      wantModel: { labels: [], values: [] }
    }
  ];

  for (const testCase of cases) {
    assert.deepEqual(totalsByAgent(testCase.rows), testCase.wantAgent, `${testCase.name}: by agent`);
    assert.deepEqual(totalsByModel(testCase.rows), testCase.wantModel, `${testCase.name}: by model`);
  }
});

test('buildStackedSeries keeps the agent dimension as one dense dataset per agent', () => {
  const rows: CostBreakdownRow[] = [
    row({ agent: 'agent-a', bucket: '2026-03-26T10:00:00Z', cost_usd: 0.03 }),
    row({ agent: 'agent-b', bucket: '2026-03-26T10:00:00Z', cost_usd: 0.04 }),
    row({ agent: 'agent-a', bucket: '2026-03-26T12:00:00Z', cost_usd: 0.08 })
  ];

  const series = buildStackedSeries(rows, 'hour');

  assert.deepEqual(series.bucketKeys, ['2026-03-26T10:00:00Z', '2026-03-26T12:00:00Z']);
  assert.equal(series.datasets.length, 2, 'one dataset per agent');

  // Stacking is only meaningful when every dataset covers every bucket, so the
  // gaps must be filled with zeroes rather than left short or sparse.
  for (const dataset of series.datasets) {
    assert.equal(
      dataset.data.length,
      series.bucketKeys.length,
      `dataset ${dataset.label} must have one value per bucket`
    );
  }

  const byAgent = new Map(series.datasets.map((dataset) => [dataset.label, dataset.data]));
  assert.deepEqual(byAgent.get('agent-a'), [0.03, 0.08]);
  assert.deepEqual(byAgent.get('agent-b'), [0.04, 0]);
});

test('buildStackedSeries orders buckets chronologically and agents by total cost', () => {
  const rows: CostBreakdownRow[] = [
    row({ agent: 'small', bucket: '2026-03-27', cost_usd: 1 }),
    row({ agent: 'large', bucket: '2026-03-26', cost_usd: 5 }),
    row({ agent: 'small', bucket: '2026-03-25', cost_usd: 1 }),
    row({ agent: 'large', bucket: '2026-03-27', cost_usd: 5 })
  ];

  const series = buildStackedSeries(rows, 'day');

  assert.deepEqual(series.bucketKeys, ['2026-03-25', '2026-03-26', '2026-03-27']);
  assert.deepEqual(
    series.datasets.map((dataset) => dataset.label),
    ['large', 'small'],
    'largest contributor first'
  );
  assert.deepEqual(series.datasets[0].data, [0, 5, 5]);
  assert.deepEqual(series.datasets[1].data, [1, 0, 1]);
});

test('buildStackedSeries folds hour buckets into local calendar days', () => {
  // America/New_York is UTC-4 in summer, so local day 2026-03-26 runs from
  // 2026-03-26T04:00:00Z through 2026-03-27T03:59:59Z. Truncating the UTC date
  // would split that single local day across two buckets.
  const rows: CostBreakdownRow[] = [
    row({ agent: 'agent-a', bucket: '2026-03-26T04:00:00Z', cost_usd: 1 }),
    row({ agent: 'agent-a', bucket: '2026-03-26T18:00:00Z', cost_usd: 2 }),
    row({ agent: 'agent-a', bucket: '2026-03-27T03:00:00Z', cost_usd: 4 }),
    row({ agent: 'agent-a', bucket: '2026-03-27T04:00:00Z', cost_usd: 8 })
  ];

  const series = buildStackedSeries(rows, 'day');

  assert.deepEqual(series.bucketKeys, ['2026-03-26', '2026-03-27']);
  assert.deepEqual(series.labels, ['Mar 26', 'Mar 27']);
  assert.deepEqual(series.datasets, [{ label: 'agent-a', data: [7, 8] }]);
});

test('buildStackedSeries assigns local midnight to the day it starts', () => {
  // 2026-03-26T04:00:00Z is exactly local midnight in America/New_York. It must
  // open 2026-03-26, and the instant one hour earlier must still be 2026-03-25.
  const rows: CostBreakdownRow[] = [
    row({ agent: 'agent-a', bucket: '2026-03-26T03:00:00Z', cost_usd: 1 }),
    row({ agent: 'agent-a', bucket: '2026-03-26T04:00:00Z', cost_usd: 2 })
  ];

  const series = buildStackedSeries(rows, 'day');

  assert.deepEqual(series.bucketKeys, ['2026-03-25', '2026-03-26']);
  assert.deepEqual(series.datasets, [{ label: 'agent-a', data: [1, 2] }]);
});

test('buildStackedSeries folds the 25-hour and 23-hour DST days correctly', () => {
  const cases: Array<{
    name: string;
    rows: CostBreakdownRow[];
    wantBucketKeys: string[];
    wantData: number[];
  }> = [
    {
      // DST ends 2026-11-01: local day 2026-11-01 is 25 hours long and runs
      // 2026-11-01T04:00:00Z through 2026-11-02T04:59:59Z.
      name: 'the long day absorbs the repeated hour',
      rows: [
        row({ agent: 'agent-a', bucket: '2026-11-01T04:00:00Z', cost_usd: 1 }),
        row({ agent: 'agent-a', bucket: '2026-11-02T04:00:00Z', cost_usd: 2 }),
        row({ agent: 'agent-a', bucket: '2026-11-02T05:00:00Z', cost_usd: 4 })
      ],
      wantBucketKeys: ['2026-11-01', '2026-11-02'],
      wantData: [3, 4]
    },
    {
      // DST starts 2026-03-08: local day 2026-03-08 is 23 hours long and runs
      // 2026-03-08T05:00:00Z through 2026-03-09T03:59:59Z.
      name: 'the short day loses the skipped hour',
      rows: [
        row({ agent: 'agent-a', bucket: '2026-03-08T04:00:00Z', cost_usd: 1 }),
        row({ agent: 'agent-a', bucket: '2026-03-08T05:00:00Z', cost_usd: 2 }),
        row({ agent: 'agent-a', bucket: '2026-03-09T03:00:00Z', cost_usd: 4 }),
        row({ agent: 'agent-a', bucket: '2026-03-09T04:00:00Z', cost_usd: 8 })
      ],
      wantBucketKeys: ['2026-03-07', '2026-03-08', '2026-03-09'],
      wantData: [1, 6, 8]
    }
  ];

  for (const testCase of cases) {
    const series = buildStackedSeries(testCase.rows, 'day');
    assert.deepEqual(series.bucketKeys, testCase.wantBucketKeys, `${testCase.name}: buckets`);
    assert.deepEqual(
      series.datasets,
      [{ label: 'agent-a', data: testCase.wantData }],
      `${testCase.name}: data`
    );
  }
});

test('buildStackedSeries keeps hour granularity unfolded', () => {
  const rows: CostBreakdownRow[] = [
    row({ agent: 'agent-a', bucket: '2026-03-26T04:00:00Z', cost_usd: 1 }),
    row({ agent: 'agent-a', bucket: '2026-03-26T05:00:00Z', cost_usd: 2 })
  ];

  const series = buildStackedSeries(rows, 'hour');

  assert.deepEqual(series.bucketKeys, ['2026-03-26T04:00:00Z', '2026-03-26T05:00:00Z']);
  assert.deepEqual(series.labels, ['00:00', '01:00']);
  assert.deepEqual(series.datasets, [{ label: 'agent-a', data: [1, 2] }]);
});

test('buildStackedSeries labels day buckets on the named calendar day', () => {
  // '2026-03-26' read as a UTC instant would render as Mar 25 in a negative-offset
  // zone, so day buckets must be read as local calendar dates.
  const series = buildStackedSeries([row({ bucket: '2026-03-26', cost_usd: 1 })], 'day');

  assert.deepEqual(series.bucketKeys, ['2026-03-26']);
  assert.deepEqual(series.labels, ['Mar 26']);
});

test('buildStackedSeries labels hour buckets in local time', () => {
  const series = buildStackedSeries(
    [
      row({ bucket: '2026-03-26T10:00:00Z', cost_usd: 1 }),
      row({ bucket: '2026-03-26T23:00:00Z', cost_usd: 1 })
    ],
    'hour'
  );

  // 10:00Z and 23:00Z are 06:00 and 19:00 in America/New_York during DST.
  assert.deepEqual(series.labels, ['06:00', '19:00']);
});

test('buildStackedSeries tolerates the awkward rows the API can return', () => {
  const cases: Array<{
    name: string;
    rows: CostBreakdownRow[];
    bucket: 'hour' | 'day';
    wantBucketKeys: string[];
    wantDatasets: Array<{ label: string; data: number[] }>;
  }> = [
    {
      name: 'no rows produce an empty series',
      rows: [],
      bucket: 'day',
      wantBucketKeys: [],
      wantDatasets: []
    },
    {
      name: 'blank agents collapse into unknown',
      rows: [row({ agent: '', bucket: '2026-03-26', cost_usd: 2 })],
      bucket: 'day',
      wantBucketKeys: ['2026-03-26'],
      wantDatasets: [{ label: 'unknown', data: [2] }]
    },
    {
      name: 'repeated agent and bucket pairs are summed',
      rows: [
        row({ agent: 'agent-a', bucket: '2026-03-26', cost_usd: 1 }),
        row({ agent: 'agent-a', bucket: '2026-03-26', cost_usd: 2 })
      ],
      bucket: 'day',
      wantBucketKeys: ['2026-03-26'],
      wantDatasets: [{ label: 'agent-a', data: [3] }]
    },
    {
      name: 'rows without a bucket cannot be placed on a time axis and are dropped',
      rows: [
        row({ agent: 'agent-a', bucket: undefined, cost_usd: 9 }),
        row({ agent: 'agent-a', bucket: '', cost_usd: 9 }),
        row({ agent: 'agent-a', bucket: '2026-03-26', cost_usd: 1 })
      ],
      bucket: 'day',
      wantBucketKeys: ['2026-03-26'],
      wantDatasets: [{ label: 'agent-a', data: [1] }]
    }
  ];

  for (const testCase of cases) {
    const series = buildStackedSeries(testCase.rows, testCase.bucket);
    assert.deepEqual(series.bucketKeys, testCase.wantBucketKeys, `${testCase.name}: buckets`);
    assert.deepEqual(
      series.datasets.map((dataset) => ({ label: dataset.label, data: dataset.data })),
      testCase.wantDatasets,
      `${testCase.name}: datasets`
    );
  }
});

test('describeStackedSeries summarises the chart for screen reader users', () => {
  const cases: Array<{ name: string; rows: CostBreakdownRow[]; rangeLabel: string; want: string }> = [
    {
      name: 'no data says so plainly',
      rows: [],
      rangeLabel: 'Today',
      want: 'No cost over time data for Today.'
    },
    {
      name: 'a single agent and bucket reads in the singular',
      rows: [row({ agent: 'agent-a', bucket: '2026-03-26T10:00:00Z', cost_usd: 1 })],
      rangeLabel: 'Today',
      want: 'Cost over time for Today, stacked by agent: 1 agent across 1 time bucket.'
    },
    {
      name: 'several agents and buckets read in the plural',
      rows: [
        row({ agent: 'agent-a', bucket: '2026-03-26T10:00:00Z', cost_usd: 1 }),
        row({ agent: 'agent-b', bucket: '2026-03-26T10:00:00Z', cost_usd: 1 }),
        row({ agent: 'agent-a', bucket: '2026-03-26T12:00:00Z', cost_usd: 1 })
      ],
      rangeLabel: 'Last 7 days',
      want: 'Cost over time for Last 7 days, stacked by agent: 2 agents across 2 time buckets.'
    }
  ];

  for (const testCase of cases) {
    const series = buildStackedSeries(testCase.rows, 'hour');
    assert.equal(describeStackedSeries(series, testCase.rangeLabel), testCase.want, testCase.name);
  }
});
