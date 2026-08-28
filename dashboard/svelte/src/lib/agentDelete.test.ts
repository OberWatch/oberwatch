import { test } from 'node:test';
import assert from 'node:assert/strict';

import { ApiError } from './api.ts';
import {
  agentDeleteClosesDialog,
  agentDeleteErrorOutcome,
  agentDeleteOutcome,
  confirmsAgentName,
  deleteAgentPath
} from './agentDelete.ts';
import type { AgentDeleteResponse } from './types.ts';

/** apiError builds the error fetchJSON throws for a given status and body. */
function apiError(status: number, message?: string): ApiError {
  const response = { status } as Response;
  return new ApiError(response, message ? { error: { code: 'x', message } } : undefined);
}

function response(overrides: Partial<AgentDeleteResponse['removed']> = {}): AgentDeleteResponse {
  return {
    status: 'deleted',
    agent: 'scratch',
    removed: {
      agent_record: 1,
      cost_records: 3,
      alerts: 2,
      budget_snapshots: 1,
      tasks_detached: 0,
      ...overrides
    },
    recreated_on_next_request: true
  };
}

test('deleteAgentPath encodes the agent name into the path', () => {
  const cases: { name: string; input: string; want: string }[] = [
    { name: 'plain name', input: 'scratch-agent', want: '/agents/scratch-agent' },
    { name: 'underscores kept', input: 'a_b', want: '/agents/a_b' },
    { name: 'space encoded', input: 'bad name', want: '/agents/bad%20name' },
    { name: 'slash encoded so it cannot add a path segment', input: 'a/rename', want: '/agents/a%2Frename' },
    { name: 'percent encoded', input: '100%', want: '/agents/100%25' }
  ];

  for (const { name, input, want } of cases) {
    assert.equal(deleteAgentPath(input), want, name);
  }
});

test('confirmsAgentName only accepts the exact name', () => {
  const cases: { name: string; typed: string; agent: string | null; want: boolean }[] = [
    { name: 'exact match confirms', typed: 'scratch', agent: 'scratch', want: true },
    { name: 'surrounding whitespace is forgiven', typed: '  scratch \n', agent: 'scratch', want: true },
    { name: 'empty box never confirms', typed: '', agent: 'scratch', want: false },
    { name: 'whitespace only never confirms', typed: '   ', agent: 'scratch', want: false },
    { name: 'prefix is not enough', typed: 'scrat', agent: 'scratch', want: false },
    { name: 'suffix is not enough', typed: 'ratch', agent: 'scratch', want: false },
    { name: 'different case is not a match', typed: 'Scratch', agent: 'scratch', want: false },
    { name: 'another agent name is not a match', typed: 'other', agent: 'scratch', want: false },
    { name: 'inner whitespace is not stripped', typed: 'scr atch', agent: 'scratch', want: false },
    { name: 'no target means nothing to confirm', typed: 'scratch', agent: null, want: false },
    { name: 'blank target cannot be confirmed by a blank box', typed: '', agent: '', want: false }
  ];

  for (const { name, typed, agent, want } of cases) {
    assert.equal(confirmsAgentName(typed, agent), want, name);
  }
});

test('a successful delete reports the counts and the rediscovery contract', () => {
  const outcome = agentDeleteOutcome('scratch', response());

  assert.equal(outcome.kind, 'deleted');
  assert.match(outcome.message, /Deleted agent "scratch"/);
  assert.match(outcome.message, /3 cost records/);
  assert.match(outcome.message, /2 alerts/);
  assert.match(
    outcome.message,
    /recreated with a fresh budget on its next request/,
    'the operator must be told the agent is not blocked'
  );
  assert.equal(agentDeleteClosesDialog(outcome), true);
});

test('counts are pluralized and a missing body still reads correctly', () => {
  assert.match(
    agentDeleteOutcome('scratch', response({ cost_records: 1, alerts: 1 })).message,
    /1 cost record, 1 alert\./,
    'exactly one row must not be reported as "1 cost records"'
  );
  assert.match(
    agentDeleteOutcome('scratch', response({ cost_records: 0, alerts: 0 })).message,
    /0 cost records, 0 alerts/,
    'zero rows still pluralize'
  );

  const bare = agentDeleteOutcome('scratch', undefined);
  assert.equal(bare.kind, 'deleted');
  assert.equal(bare.message, 'Deleted agent "scratch".');
});

test('a 404 is the already-deleted case, not a failure', () => {
  const outcome = agentDeleteErrorOutcome('scratch', apiError(404, 'agent not found'));

  assert.equal(outcome.kind, 'already_deleted');
  assert.match(outcome.message, /was already deleted/);
  assert.equal(agentDeleteClosesDialog(outcome), true, 'the dialog must close: the agent is gone');
});

test('every other failure keeps the dialog open with the server explanation', () => {
  const cases: { name: string; error: unknown; want: RegExp }[] = [
    {
      name: 'protected agent explains the config',
      error: apiError(
        409,
        'agent is defined in the configuration file and cannot be deleted from the dashboard; remove it from the config instead'
      ),
      want: /remove it from the config instead/
    },
    {
      name: 'unauthenticated surfaces the session message',
      error: apiError(401, 'Missing or invalid session'),
      want: /Missing or invalid session/
    },
    {
      name: 'server error surfaces the status when there is no body',
      error: apiError(500),
      want: /status 500/
    },
    {
      name: 'a network failure falls back to a readable message',
      error: new TypeError('Failed to fetch'),
      want: /Failed to fetch/
    },
    {
      name: 'a non-error rejection still names the agent',
      error: 'boom',
      want: /Failed to delete agent "scratch"/
    }
  ];

  for (const { name, error, want } of cases) {
    const outcome = agentDeleteErrorOutcome('scratch', error);
    assert.equal(outcome.kind, 'failed', name);
    assert.match(outcome.message, want, name);
    assert.equal(agentDeleteClosesDialog(outcome), false, `${name}: the dialog must stay open`);
  }
});
