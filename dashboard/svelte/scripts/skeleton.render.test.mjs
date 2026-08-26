/**
 * Rendering tests for the shared loading and error state components.
 *
 * Every assertion runs against markup produced by the real Svelte compiler and
 * server renderer, so props, accessible semantics and the reduced-motion class
 * are checked as rendered output rather than as source text.
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';

import { countOccurrences, render } from './lib/render-svelte.mjs';

const SKELETON = 'lib/components/Skeleton.svelte';
const KPI = 'lib/components/SkeletonKPICard.svelte';
const TABLE = 'lib/components/SkeletonTable.svelte';
const CHART = 'lib/components/SkeletonChart.svelte';
const ERROR_STATE = 'lib/components/ErrorState.svelte';

/** Bars are identified by the one class every skeleton bar must carry. */
const PULSE = 'motion-safe:animate-pulse';

function assertNoForcedPulse(markup) {
  const pulses = countOccurrences(markup, 'animate-pulse');
  const guarded = countOccurrences(markup, PULSE);
  assert.equal(
    pulses,
    guarded,
    `every animate-pulse must be gated behind motion-safe:, found ${pulses - guarded} unguarded`
  );
  assert.ok(guarded > 0, 'a skeleton must actually pulse for users who allow motion');
}

test('Skeleton sizes and rounds itself from props', async () => {
  const markup = await render(SKELETON, { width: '120px', height: '8px', rounded: 'full' });

  assert.match(markup, /width:\s*120px/, 'the width prop must reach the element');
  assert.match(markup, /height:\s*8px/, 'the height prop must reach the element');
  assert.match(markup, /\brounded-full\b/, 'the rounded prop must pick the radius utility');
});

test('Skeleton has usable defaults and every rounding option', async () => {
  const defaults = await render(SKELETON);
  assert.match(defaults, /width:\s*100%/, 'a bar fills its container by default');
  assert.match(defaults, /height:\s*1rem/, 'a bar has a default height');
  assert.match(defaults, /\brounded-md\b/, 'a bar is rounded by default');

  for (const [rounded, expected] of [
    ['none', 'rounded-none'],
    ['sm', 'rounded-sm'],
    ['md', 'rounded-md'],
    ['lg', 'rounded-lg'],
    ['full', 'rounded-full']
  ]) {
    const markup = await render(SKELETON, { rounded });
    assert.match(markup, new RegExp(`\\b${expected}\\b`), `rounded="${rounded}" must render ${expected}`);
  }
});

test('Skeleton pulses on the elevated surface and never forces motion', async () => {
  const markup = await render(SKELETON);

  assert.match(markup, /\bbg-elevated\b/, 'skeletons use the elevated surface colour');
  assertNoForcedPulse(markup);
});

test('Skeleton bars are decorative so the group label is announced once', async () => {
  const markup = await render(SKELETON);
  assert.match(markup, /aria-hidden="true"/, 'an individual bar carries no meaning of its own');
});

test('SkeletonKPICard announces itself as a busy status region', async () => {
  const markup = await render(KPI, { label: 'Loading total spend today' });

  assert.match(markup, /role="status"/, 'a loading placeholder is a status region');
  assert.match(markup, /aria-busy="true"/, 'the region must report that it is still loading');
  assert.match(markup, /Loading total spend today/, 'the label prop must be rendered');
  assert.match(markup, /\bsr-only\b/, 'the label is for assistive technology only');
  assertNoForcedPulse(markup);
});

test('SkeletonKPICard mirrors the KPI card shape', async () => {
  const markup = await render(KPI);

  assert.match(markup, /Loading/, 'the label has a sensible default');
  assert.ok(
    countOccurrences(markup, PULSE) >= 2,
    'a KPI placeholder stands in for a title and a value'
  );
  assert.match(markup, /\bbg-surface\b/, 'the placeholder keeps the card surface');
  assert.match(markup, /border-border-default/, 'the placeholder keeps the card border');
});

test('SkeletonTable renders the requested number of rows and columns', async () => {
  const markup = await render(TABLE, { rows: 3, columns: 4, label: 'Loading agents' });

  assert.equal(
    countOccurrences(markup, PULSE),
    3 * 4,
    'the rows and columns props must control how many bars are drawn'
  );
  assert.match(markup, /role="status"/);
  assert.match(markup, /aria-busy="true"/);
  assert.match(markup, /Loading agents/, 'the label prop must be rendered');
  assertNoForcedPulse(markup);
});

test('SkeletonTable defaults to a table-shaped placeholder', async () => {
  const markup = await render(TABLE);

  assert.ok(countOccurrences(markup, PULSE) > 1, 'the default placeholder has several rows');
  assert.match(markup, /Loading/, 'the label has a sensible default');
});

test('SkeletonChart reserves the chart height it is given', async () => {
  const markup = await render(CHART, { height: 340, label: 'Loading cost trend' });

  assert.match(markup, /height:\s*340px/, 'the height prop must reserve the real chart height');
  assert.match(markup, /role="status"/);
  assert.match(markup, /aria-busy="true"/);
  assert.match(markup, /Loading cost trend/, 'the label prop must be rendered');
  assertNoForcedPulse(markup);
});

test('SkeletonChart has a default height so layout does not jump', async () => {
  const markup = await render(CHART);
  assert.match(markup, /height:\s*\d+px/, 'a default height must still be reserved');
});

test('ErrorState shows the API message and a retry control', async () => {
  const markup = await render(ERROR_STATE, {
    message: 'costs request failed: 503',
    onRetry: () => {}
  });

  assert.match(markup, /role="alert"/, 'an API failure must be announced');
  assert.match(markup, /costs request failed: 503/, 'the real API message must be shown');
  assert.match(markup, /<button[^>]*type="button"/, 'retry must be a real button');
  assert.match(markup, />\s*Retry\s*</, 'the retry control must be labelled Retry');
  assert.doesNotMatch(markup, /animate-pulse/, 'an error state is not a loading state');
});

test('ErrorState allows a caller-specific retry label', async () => {
  const markup = await render(ERROR_STATE, {
    message: 'nope',
    retryLabel: 'Retry loading agents',
    onRetry: () => {}
  });

  assert.match(markup, />\s*Retry loading agents\s*</, 'callers can name what is retried');
});
