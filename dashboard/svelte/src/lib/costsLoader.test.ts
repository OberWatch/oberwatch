process.env.TZ = 'America/New_York';

import { test } from 'node:test';
import assert from 'node:assert/strict';

import { createCostsLoader, type CostsFetcher } from './costsLoader.ts';
import type { DateRangeSelection } from './dateRange.ts';
import type { CostsResponse } from './types.ts';

const NOW = new Date('2026-08-26T18:30:00.000Z');

function selection(overrides: Partial<DateRangeSelection> = {}): DateRangeSelection {
  return { preset: 'today', customStart: '', customEnd: '', ...overrides };
}

function response(totalUSD: number, agent: string): CostsResponse {
  return {
    total_usd: totalUSD,
    total_requests: 1,
    total_input_tokens: 1,
    total_output_tokens: 1,
    breakdown: [
      {
        agent,
        model: 'gpt-4o',
        bucket: '2026-08-26T12:00:00Z',
        requests: 1,
        input_tokens: 1,
        output_tokens: 1,
        cost_usd: totalUSD
      }
    ]
  };
}

interface Deferred {
  promise: Promise<CostsResponse>;
  resolve: (value: CostsResponse) => void;
  reject: (reason: Error) => void;
}

function deferred(): Deferred {
  let resolve: (value: CostsResponse) => void = () => {};
  let reject: (reason: Error) => void = () => {};
  const promise = new Promise<CostsResponse>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

/**
 * queuedFetcher hands out one deferred per call so a test can resolve requests in
 * any order it likes.
 */
function queuedFetcher(): { fetcher: CostsFetcher; calls: string[]; pending: Deferred[] } {
  const calls: string[] = [];
  const pending: Deferred[] = [];
  const fetcher: CostsFetcher = (query) => {
    calls.push(query);
    const next = deferred();
    pending.push(next);
    return next.promise;
  };
  return { fetcher, calls, pending };
}

test('a superseded load cannot overwrite the newer selection', async () => {
  const { fetcher, pending } = queuedFetcher();
  const loader = createCostsLoader(fetcher, () => NOW);

  // The user picks 30d, then switches to Today before 30d comes back.
  const first = loader.load(selection({ preset: '30d' }));
  const second = loader.load(selection({ preset: 'today' }));

  // Each load issues a totals request and a series request.
  assert.equal(pending.length, 4, 'two loads issue two requests each');

  // The newer load answers first, then the older one finally lands.
  pending[2].resolve(response(2, 'today-agent'));
  pending[3].resolve(response(2, 'today-agent'));
  const secondOutcome = await second;

  pending[0].resolve(response(99, 'stale-agent'));
  pending[1].resolve(response(99, 'stale-agent'));
  const firstOutcome = await first;

  assert.equal(firstOutcome.status, 'stale', 'the superseded load must be discarded');
  assert.equal(secondOutcome.status, 'loaded');
  assert.ok(secondOutcome.status === 'loaded');
  assert.equal(secondOutcome.totalUSD, 2);
  assert.equal(secondOutcome.range.preset, 'today');
});

test('a superseded load that fails cannot raise an error over the newer selection', async () => {
  const { fetcher, pending } = queuedFetcher();
  const loader = createCostsLoader(fetcher, () => NOW);

  const first = loader.load(selection({ preset: '30d' }));
  const second = loader.load(selection({ preset: 'today' }));

  pending[2].resolve(response(5, 'today-agent'));
  pending[3].resolve(response(5, 'today-agent'));
  const secondOutcome = await second;

  pending[0].reject(new Error('network is down'));
  pending[1].reject(new Error('network is down'));
  const firstOutcome = await first;

  assert.equal(firstOutcome.status, 'stale', 'a stale failure must not surface');
  assert.equal(secondOutcome.status, 'loaded');
});

test('the newest load wins even when the older one is the last to be started', async () => {
  const { fetcher, pending } = queuedFetcher();
  const loader = createCostsLoader(fetcher, () => NOW);

  const first = loader.load(selection({ preset: 'today' }));
  const second = loader.load(selection({ preset: '7d' }));
  const third = loader.load(selection({ preset: '30d' }));

  // Resolve them in the order they were started; only the last one may apply.
  pending[0].resolve(response(1, 'a'));
  pending[1].resolve(response(1, 'a'));
  pending[2].resolve(response(2, 'b'));
  pending[3].resolve(response(2, 'b'));
  pending[4].resolve(response(3, 'c'));
  pending[5].resolve(response(3, 'c'));

  const outcomes = await Promise.all([first, second, third]);

  assert.deepEqual(
    outcomes.map((outcome) => outcome.status),
    ['stale', 'stale', 'loaded']
  );
  const last = outcomes[2];
  assert.ok(last.status === 'loaded');
  assert.equal(last.range.preset, '30d');
});

test('an invalid custom range reports the validation message and issues no request', async () => {
  const { fetcher, calls } = queuedFetcher();
  const loader = createCostsLoader(fetcher, () => NOW);

  const outcome = await loader.load(selection({ preset: 'custom', customStart: '', customEnd: '' }));

  assert.equal(outcome.status, 'invalid');
  assert.ok(outcome.status === 'invalid');
  assert.equal(outcome.error, 'Select a start date.');
  assert.deepEqual(calls, [], 'a rejected range must not be queried');
});

test('an invalid selection supersedes a load that is still in flight', async () => {
  const { fetcher, pending } = queuedFetcher();
  const loader = createCostsLoader(fetcher, () => NOW);

  const first = loader.load(selection({ preset: '30d' }));
  const invalid = await loader.load(
    selection({ preset: 'custom', customStart: '2026-08-11', customEnd: '2026-08-10' })
  );

  pending[0].resolve(response(7, 'stale-agent'));
  pending[1].resolve(response(7, 'stale-agent'));
  const firstOutcome = await first;

  assert.equal(invalid.status, 'invalid');
  assert.ok(invalid.status === 'invalid');
  assert.equal(invalid.error, 'Start date must be on or before the end date.');
  assert.equal(firstOutcome.status, 'stale', 'the in-flight load no longer matches the selection');
});

test('a load queries the totals and the series over one identical window', async () => {
  const { fetcher, calls, pending } = queuedFetcher();
  const loader = createCostsLoader(fetcher, () => NOW);

  const pendingLoad = loader.load(selection({ preset: '7d' }));
  pending[0].resolve(response(1, 'agent-a'));
  pending[1].resolve(response(1, 'agent-a'));
  const outcome = await pendingLoad;

  assert.ok(outcome.status === 'loaded');
  assert.equal(calls.length, 2);

  const instants = calls.map((call) => call.slice(call.indexOf('from=')));
  assert.equal(instants[0], instants[1], 'totals and series must share the exact instants');
  assert.ok(calls[0].includes('group_by=none'), 'totals use the raw-row grouping');
  assert.ok(calls[1].includes('group_by=agent_hour'), 'the series keeps the agent dimension');
  assert.equal(outcome.rows.length, 1);
  assert.equal(outcome.seriesRows.length, 1);
});

test('a failing load that is still current reports the failure and its range', async () => {
  const { fetcher, pending } = queuedFetcher();
  const loader = createCostsLoader(fetcher, () => NOW);

  const pendingLoad = loader.load(selection({ preset: 'today' }));
  pending[0].reject(new Error('storage is not configured'));
  pending[1].reject(new Error('storage is not configured'));
  const outcome = await pendingLoad;

  assert.equal(outcome.status, 'failed');
  assert.ok(outcome.status === 'failed');
  assert.equal(outcome.error, 'storage is not configured');
  assert.equal(outcome.range.preset, 'today');
});
