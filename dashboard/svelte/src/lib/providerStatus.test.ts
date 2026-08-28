import { test } from 'node:test';
import assert from 'node:assert/strict';

import {
  PROVIDER_REFRESH_INTERVAL_MS,
  isProviderPending,
  nextProviderRows,
  providerBadgeStatus,
  providerObservedAt,
  providerStatusLabel
} from './providerStatus.ts';
import type { ProviderStatus } from './types.ts';

function row(overrides: Partial<ProviderStatus> & Pick<ProviderStatus, 'provider'>): ProviderStatus {
  return {
    label: overrides.provider,
    status: 'operational',
    public: true,
    detail: 'test',
    observed_at: '2026-08-28T10:00:00Z',
    ...overrides
  };
}

const openai = row({ provider: 'openai', label: 'OpenAI' });
const anthropic = row({ provider: 'anthropic', label: 'Anthropic' });
const ollamaUp = row({ provider: 'ollama', label: 'Ollama (local)', public: false });
const ollamaDown = row({
  provider: 'ollama',
  label: 'Ollama (local)',
  public: false,
  status: 'unreachable'
});

test('providerStatusLabel names every status truthfully and reports pending rows as checking', () => {
  const cases: { name: string; input: ProviderStatus; want: string }[] = [
    { name: 'operational', input: row({ provider: 'x', status: 'operational' }), want: 'Operational' },
    { name: 'degraded', input: row({ provider: 'x', status: 'degraded' }), want: 'Degraded' },
    { name: 'outage', input: row({ provider: 'x', status: 'outage' }), want: 'Outage' },
    {
      name: 'public feed could not be read',
      input: row({ provider: 'x', status: 'status_unavailable' }),
      want: 'Status unavailable'
    },
    {
      name: 'local server did not answer',
      input: row({ provider: 'x', status: 'unreachable' }),
      want: 'Unreachable'
    },
    {
      name: 'never probed is checking, not a verdict',
      input: row({ provider: 'x', status: 'unreachable', observed_at: undefined }),
      want: 'Checking'
    },
    {
      name: 'never probed public row is checking too',
      input: row({ provider: 'x', status: 'status_unavailable', observed_at: '' }),
      want: 'Checking'
    }
  ];

  for (const c of cases) {
    assert.equal(providerStatusLabel(c.input), c.want, c.name);
  }
});

test('providerBadgeStatus maps each availability to one badge colour', () => {
  const cases: { status: ProviderStatus['status']; want: string }[] = [
    { status: 'operational', want: 'success' },
    { status: 'degraded', want: 'warning' },
    { status: 'outage', want: 'error' },
    { status: 'status_unavailable', want: 'error' },
    { status: 'unreachable', want: 'error' }
  ];
  for (const c of cases) {
    assert.equal(providerBadgeStatus(c.status), c.want, c.status);
  }
});

test('isProviderPending is true only without an observed_at', () => {
  assert.equal(isProviderPending(row({ provider: 'x', observed_at: undefined })), true);
  assert.equal(isProviderPending(row({ provider: 'x', observed_at: '' })), true);
  assert.equal(isProviderPending(row({ provider: 'x' })), false);
});

test('nextProviderRows keeps every card through failure and recovery', () => {
  const cases: {
    name: string;
    previous: ProviderStatus[];
    result: Parameters<typeof nextProviderRows>[1];
    wantProviders: string[];
    wantOllamaStatus?: string;
  }[] = [
    {
      name: 'ollama goes down: the card stays and shows unreachable',
      previous: [openai, anthropic, ollamaUp],
      result: { ok: true, rows: [openai, anthropic, ollamaDown] },
      wantProviders: ['openai', 'anthropic', 'ollama'],
      wantOllamaStatus: 'unreachable'
    },
    {
      name: 'ollama comes back: the card recovers in place',
      previous: [openai, anthropic, ollamaDown],
      result: { ok: true, rows: [openai, anthropic, ollamaUp] },
      wantProviders: ['openai', 'anthropic', 'ollama'],
      wantOllamaStatus: 'operational'
    },
    {
      name: 'a failed refresh keeps what was on screen instead of blanking the grid',
      previous: [openai, anthropic, ollamaDown],
      result: { ok: false },
      wantProviders: ['openai', 'anthropic', 'ollama'],
      wantOllamaStatus: 'unreachable'
    },
    {
      name: 'a failed first refresh over no rows stays at no rows, not an error verdict',
      previous: [],
      result: { ok: false },
      wantProviders: []
    },
    {
      name: 'no ollama configured never grows an ollama card',
      previous: [openai, anthropic],
      result: { ok: true, rows: [openai, anthropic] },
      wantProviders: ['openai', 'anthropic']
    },
    {
      name: 'public feeds failing keep both public cards',
      previous: [openai, anthropic],
      result: {
        ok: true,
        rows: [
          row({ provider: 'openai', status: 'status_unavailable' }),
          row({ provider: 'anthropic', status: 'status_unavailable' })
        ]
      },
      wantProviders: ['openai', 'anthropic']
    }
  ];

  for (const c of cases) {
    const got = nextProviderRows(c.previous, c.result);
    assert.deepEqual(
      got.map((r) => r.provider),
      c.wantProviders,
      `${c.name}: card set`
    );
    if (c.wantOllamaStatus) {
      const ollama = got.find((r) => r.provider === 'ollama');
      assert.ok(ollama, `${c.name}: ollama card present`);
      assert.equal(ollama.status, c.wantOllamaStatus, `${c.name}: ollama status`);
    }
  }
});

test('nextProviderRows returns the same array on failure so nothing re-renders as new', () => {
  const previous = [openai, anthropic];
  assert.equal(nextProviderRows(previous, { ok: false }), previous);
});

test('providerObservedAt reports the latest probe time and null before the first probe', () => {
  assert.equal(providerObservedAt([]), null, 'no rows');
  assert.equal(
    providerObservedAt([row({ provider: 'x', observed_at: undefined })]),
    null,
    'pending rows'
  );
  assert.equal(
    providerObservedAt([row({ provider: 'x', observed_at: 'not a date' })]),
    null,
    'unparseable timestamps are not a time'
  );

  const got = providerObservedAt([
    row({ provider: 'a', observed_at: '2026-08-28T10:00:00Z' }),
    row({ provider: 'b', observed_at: '2026-08-28T10:05:00Z' }),
    row({ provider: 'c', observed_at: undefined })
  ]);
  assert.ok(got);
  assert.equal(got.toISOString(), '2026-08-28T10:05:00.000Z');
});

test('the refresh interval is short enough to notice a recovery within a minute or two, and never tighter than the probe timeout', () => {
  assert.ok(PROVIDER_REFRESH_INTERVAL_MS >= 5_000, 'must not hammer the API');
  assert.ok(PROVIDER_REFRESH_INTERVAL_MS <= 60_000, 'must pick up the server snapshot within its TTL');
});
