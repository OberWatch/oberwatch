import { test } from 'node:test';
import assert from 'node:assert/strict';

import { loadPhase, type LoadPhase } from './loadState.ts';

test('loadPhase separates loading, API failure, loaded-empty and loaded-with-data', () => {
  const cases: {
    name: string;
    loading: boolean;
    errorMessage: string | null;
    hasData: boolean;
    want: LoadPhase;
  }[] = [
    {
      name: 'first load in flight shows placeholders, not a zero result',
      loading: true,
      errorMessage: null,
      hasData: false,
      want: 'loading'
    },
    {
      name: 'a refetch over existing data still shows placeholders',
      loading: true,
      errorMessage: null,
      hasData: true,
      want: 'loading'
    },
    {
      name: 'a reload after a failure shows placeholders, not the stale error',
      loading: true,
      errorMessage: 'costs request failed: 503',
      hasData: false,
      want: 'loading'
    },
    {
      name: 'a failed load is an error, never an empty result',
      loading: false,
      errorMessage: 'costs request failed: 503',
      hasData: false,
      want: 'error'
    },
    {
      name: 'a failure that left stale rows behind is still an error',
      loading: false,
      errorMessage: 'agents request failed: 500',
      hasData: true,
      want: 'error'
    },
    {
      name: 'a successful load with nothing in it is genuinely empty',
      loading: false,
      errorMessage: null,
      hasData: false,
      want: 'empty'
    },
    {
      name: 'a successful load with rows is ready',
      loading: false,
      errorMessage: null,
      hasData: true,
      want: 'ready'
    }
  ];

  for (const testCase of cases) {
    const got = loadPhase({
      loading: testCase.loading,
      errorMessage: testCase.errorMessage,
      hasData: testCase.hasData
    });
    assert.equal(got, testCase.want, testCase.name);
  }
});

test('loadPhase treats an empty error string as no error', () => {
  assert.equal(
    loadPhase({ loading: false, errorMessage: '', hasData: false }),
    'empty',
    'an empty message must not render an error banner with no text in it'
  );
});
