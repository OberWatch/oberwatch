// Pin the zone before any Date use so local-calendar assertions are deterministic.
// America/New_York observes DST, which is what makes the calendar-arithmetic cases meaningful.
process.env.TZ = 'America/New_York';

import { test } from 'node:test';
import assert from 'node:assert/strict';

import {
  canExportSelection,
  costsExportQuery,
  costsQuery,
  resolveRange,
  seriesQuery,
  type DateRangeSelection
} from './dateRange.ts';

function selection(overrides: Partial<DateRangeSelection> = {}): DateRangeSelection {
  return { preset: 'today', customStart: '', customEnd: '', ...overrides };
}

test('resolveRange anchors Today to the local calendar day start expressed in UTC', () => {
  const cases: Array<{ name: string; now: string; wantFrom: string }> = [
    {
      name: 'afternoon in daylight saving time',
      now: '2026-08-26T18:30:00.000Z',
      wantFrom: '2026-08-26T04:00:00.000Z'
    },
    {
      // 03:00Z is still the previous local day in New York. A now-minus-24h window
      // would answer 2026-08-25T03:00:00Z, which is not a calendar-day start.
      name: 'after midnight UTC but still the previous local day',
      now: '2026-08-26T03:00:00.000Z',
      wantFrom: '2026-08-25T04:00:00.000Z'
    },
    {
      name: 'standard time keeps the same calendar rule at a different offset',
      now: '2026-01-15T12:00:00.000Z',
      wantFrom: '2026-01-15T05:00:00.000Z'
    }
  ];

  for (const testCase of cases) {
    const now = new Date(testCase.now);
    const resolution = resolveRange(selection({ preset: 'today' }), now);

    assert.equal(resolution.ok, true, `${testCase.name}: expected a valid range`);
    assert.ok(resolution.ok);
    assert.equal(resolution.range.fromISO, testCase.wantFrom, testCase.name);
    assert.equal(resolution.range.toISO, now.toISOString(), `${testCase.name}: to is the current instant`);
    assert.equal(resolution.range.bucket, 'hour', `${testCase.name}: today buckets hourly`);
  }
});

test('resolveRange counts 7d and 30d as calendar days ending today', () => {
  const cases: Array<{ name: string; preset: '7d' | '30d'; now: string; wantFrom: string }> = [
    {
      name: '7d covers today plus the six preceding calendar days',
      preset: '7d',
      now: '2026-08-26T18:30:00.000Z',
      wantFrom: '2026-08-20T04:00:00.000Z'
    },
    {
      name: '30d covers today plus the twenty-nine preceding calendar days',
      preset: '30d',
      now: '2026-08-26T18:30:00.000Z',
      wantFrom: '2026-07-28T04:00:00.000Z'
    },
    {
      // The window spans the 2026-11-01 DST end. Subtracting 6 * 24h from local
      // midnight would land on 2026-10-28T05:00:00Z and lose an hour of data.
      name: '7d stays on a calendar-day start across a DST transition',
      preset: '7d',
      now: '2026-11-03T15:00:00.000Z',
      wantFrom: '2026-10-28T04:00:00.000Z'
    },
    {
      name: '30d crosses a month boundary on a calendar-day start',
      preset: '30d',
      now: '2026-03-10T16:00:00.000Z',
      wantFrom: '2026-02-09T05:00:00.000Z'
    }
  ];

  for (const testCase of cases) {
    const now = new Date(testCase.now);
    const resolution = resolveRange(selection({ preset: testCase.preset }), now);

    assert.equal(resolution.ok, true, `${testCase.name}: expected a valid range`);
    assert.ok(resolution.ok);
    assert.equal(resolution.range.fromISO, testCase.wantFrom, testCase.name);
    assert.equal(resolution.range.toISO, now.toISOString(), `${testCase.name}: to is the current instant`);
    assert.equal(resolution.range.bucket, 'day', `${testCase.name}: multi-day presets bucket daily`);
  }
});

test('resolveRange rejects incomplete or reversed custom ranges', () => {
  const now = new Date('2026-08-26T18:30:00.000Z');
  const cases: Array<{ name: string; start: string; end: string; wantError: string }> = [
    {
      name: 'missing start',
      start: '',
      end: '2026-08-10',
      wantError: 'Select a start date.'
    },
    {
      name: 'missing end',
      start: '2026-08-10',
      end: '',
      wantError: 'Select an end date.'
    },
    {
      name: 'both missing reports the start first',
      start: '',
      end: '',
      wantError: 'Select a start date.'
    },
    {
      name: 'unparseable start',
      start: 'not-a-date',
      end: '2026-08-10',
      wantError: 'Enter a valid start date.'
    },
    {
      name: 'unparseable end',
      start: '2026-08-10',
      end: '2026-13-45',
      wantError: 'Enter a valid end date.'
    },
    {
      name: 'start after end',
      start: '2026-08-11',
      end: '2026-08-10',
      wantError: 'Start date must be on or before the end date.'
    }
  ];

  for (const testCase of cases) {
    const resolution = resolveRange(
      selection({ preset: 'custom', customStart: testCase.start, customEnd: testCase.end }),
      now
    );

    assert.equal(resolution.ok, false, `${testCase.name}: expected a validation failure`);
    assert.ok(!resolution.ok);
    assert.equal(resolution.error, testCase.wantError, testCase.name);
  }
});

test('resolveRange turns accepted custom dates into exact inclusive instants', () => {
  const now = new Date('2026-08-26T18:30:00.000Z');
  const cases: Array<{
    name: string;
    start: string;
    end: string;
    wantFrom: string;
    wantTo: string;
    wantBucket: 'hour' | 'day';
  }> = [
    {
      name: 'single day is a full local day and buckets hourly',
      start: '2026-08-01',
      end: '2026-08-01',
      wantFrom: '2026-08-01T04:00:00.000Z',
      wantTo: '2026-08-02T03:59:59.999Z',
      wantBucket: 'hour'
    },
    {
      name: 'two days still bucket hourly',
      start: '2026-08-01',
      end: '2026-08-02',
      wantFrom: '2026-08-01T04:00:00.000Z',
      wantTo: '2026-08-03T03:59:59.999Z',
      wantBucket: 'hour'
    },
    {
      name: 'three days or more bucket daily',
      start: '2026-08-01',
      end: '2026-08-03',
      wantFrom: '2026-08-01T04:00:00.000Z',
      wantTo: '2026-08-04T03:59:59.999Z',
      wantBucket: 'day'
    },
    {
      name: 'range spanning a DST end keeps both local day boundaries',
      start: '2026-10-30',
      end: '2026-11-02',
      wantFrom: '2026-10-30T04:00:00.000Z',
      wantTo: '2026-11-03T04:59:59.999Z',
      wantBucket: 'day'
    },
    {
      name: 'custom end in the future is preserved verbatim',
      start: '2026-09-01',
      end: '2026-09-30',
      wantFrom: '2026-09-01T04:00:00.000Z',
      wantTo: '2026-10-01T03:59:59.999Z',
      wantBucket: 'day'
    }
  ];

  for (const testCase of cases) {
    const resolution = resolveRange(
      selection({ preset: 'custom', customStart: testCase.start, customEnd: testCase.end }),
      now
    );

    assert.equal(resolution.ok, true, `${testCase.name}: expected a valid range`);
    assert.ok(resolution.ok);
    assert.equal(resolution.range.fromISO, testCase.wantFrom, `${testCase.name}: from`);
    assert.equal(resolution.range.toISO, testCase.wantTo, `${testCase.name}: to`);
    assert.equal(resolution.range.bucket, testCase.wantBucket, `${testCase.name}: bucket`);
  }
});

test('the series is always fetched as agent-by-hour instants', () => {
  // Day buckets are folded on the client, because only the client knows the
  // viewer's calendar. Asking the API for day buckets would truncate a UTC date,
  // which splits a local day in any negative UTC offset.
  const now = new Date('2026-08-26T18:30:00.000Z');
  const presets: Array<'today' | '7d' | '30d'> = ['today', '7d', '30d'];

  for (const preset of presets) {
    const resolution = resolveRange(selection({ preset }), now);
    assert.ok(resolution.ok);
    assert.ok(
      seriesQuery(resolution.range).startsWith('group_by=agent_hour&'),
      `${preset} must request hourly instants, got ${seriesQuery(resolution.range)}`
    );
  }

  const custom = resolveRange(
    selection({ preset: 'custom', customStart: '2026-08-01', customEnd: '2026-08-31' }),
    now
  );
  assert.ok(custom.ok);
  assert.equal(custom.range.bucket, 'day', 'a month-long custom range still folds into days');
  assert.ok(
    seriesQuery(custom.range).startsWith('group_by=agent_hour&'),
    'a day-bucketed range still fetches hourly instants'
  );
});

test('one resolved range drives the totals, series and export queries identically', () => {
  const now = new Date('2026-08-26T18:30:00.000Z');
  const cases: Array<{ name: string; selection: DateRangeSelection; wantGroupBy: string }> = [
    {
      name: 'today',
      selection: selection({ preset: 'today' }),
      wantGroupBy: 'agent_hour'
    },
    {
      name: '30d',
      selection: selection({ preset: '30d' }),
      wantGroupBy: 'agent_hour'
    },
    {
      name: 'custom',
      selection: selection({ preset: 'custom', customStart: '2026-08-01', customEnd: '2026-08-03' }),
      wantGroupBy: 'agent_hour'
    }
  ];

  for (const testCase of cases) {
    const resolution = resolveRange(testCase.selection, now);
    assert.ok(resolution.ok, `${testCase.name}: expected a valid range`);
    const range = resolution.range;

    const totals = costsQuery(range);
    const series = seriesQuery(range);
    const exported = costsExportQuery(range);

    const expectedInstants = `from=${encodeURIComponent(range.fromISO)}&to=${encodeURIComponent(range.toISO)}`;
    for (const [label, query] of [
      ['totals', totals],
      ['series', series],
      ['export', exported]
    ] as const) {
      assert.ok(
        query.includes(expectedInstants),
        `${testCase.name}: ${label} query must carry the resolved instants, got ${query}`
      );
    }

    // Totals, table and CSV must be the same aggregation over the same window.
    assert.equal(totals, exported, `${testCase.name}: export query must match the totals query`);
    assert.ok(totals.includes('group_by=none'), `${testCase.name}: totals keep the raw-row grouping`);
    assert.ok(
      series.includes(`group_by=${testCase.wantGroupBy}`),
      `${testCase.name}: series uses ${testCase.wantGroupBy}, got ${series}`
    );
  }
});

test('resolveRange reports the preset and a human label for the selected window', () => {
  const now = new Date('2026-08-26T18:30:00.000Z');
  const cases: Array<{ selection: DateRangeSelection; wantPreset: string; wantLabel: string }> = [
    { selection: selection({ preset: 'today' }), wantPreset: 'today', wantLabel: 'Today' },
    { selection: selection({ preset: '7d' }), wantPreset: '7d', wantLabel: 'Last 7 days' },
    { selection: selection({ preset: '30d' }), wantPreset: '30d', wantLabel: 'Last 30 days' },
    {
      selection: selection({ preset: 'custom', customStart: '2026-08-01', customEnd: '2026-08-03' }),
      wantPreset: 'custom',
      wantLabel: '2026-08-01 to 2026-08-03'
    }
  ];

  for (const testCase of cases) {
    const resolution = resolveRange(testCase.selection, now);
    assert.ok(resolution.ok);
    assert.equal(resolution.range.preset, testCase.wantPreset);
    assert.equal(resolution.range.label, testCase.wantLabel);
  }
});

test('the CSV export is only offered for the range the page is actually showing', () => {
  const today = selection({ preset: 'today' });
  const thirtyDays = selection({ preset: '30d' });
  const customA = selection({ preset: 'custom', customStart: '2026-08-01', customEnd: '2026-08-03' });
  const customB = selection({ preset: 'custom', customStart: '2026-08-01', customEnd: '2026-08-04' });

  const cases: Array<{ name: string; selected: DateRangeSelection; loaded: DateRangeSelection | null; want: boolean }> = [
    {
      name: 'nothing has loaded yet',
      selected: today,
      loaded: null,
      want: false
    },
    {
      name: 'the loaded range is the selected range',
      selected: today,
      loaded: today,
      want: true
    },
    {
      // The defect: the user switches to 30d and the page still holds the Today
      // range until the new load lands. Exporting in that gap wrote the old range.
      name: 'a newer preset is selected but the old range is still loaded',
      selected: thirtyDays,
      loaded: today,
      want: false
    },
    {
      name: 'custom dates have been edited since the last load',
      selected: customB,
      loaded: customA,
      want: false
    },
    {
      name: 'an unapplied custom draft does not match the loaded preset',
      selected: customA,
      loaded: today,
      want: false
    },
    {
      name: 'the same custom range counts as loaded',
      selected: customA,
      loaded: { ...customA },
      want: true
    },
    {
      name: 'surrounding whitespace in custom inputs is not a different range',
      selected: selection({ preset: 'custom', customStart: ' 2026-08-01 ', customEnd: '2026-08-03' }),
      loaded: customA,
      want: true
    },
    {
      name: 'stale custom text behind a preset selection is irrelevant',
      selected: selection({ preset: '7d', customStart: '2026-01-01', customEnd: '2026-01-02' }),
      loaded: selection({ preset: '7d' }),
      want: true
    }
  ];

  for (const testCase of cases) {
    assert.equal(
      canExportSelection(testCase.selected, testCase.loaded),
      testCase.want,
      testCase.name
    );
  }
});
