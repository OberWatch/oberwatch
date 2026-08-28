import type { AgentDeleteResponse } from './types';

/**
 * What the Agents page shows after one delete attempt.
 *
 * `already_deleted` is separate from `failed` because the list on screen can be
 * stale: another dashboard, or a direct API call, may have removed the agent
 * first. A 404 then means the agent is gone, which is what the operator asked
 * for, so reporting it as an error would put a red message next to a row that
 * is about to disappear anyway.
 */
export type AgentDeleteOutcome =
  | { kind: 'deleted'; message: string }
  | { kind: 'already_deleted'; message: string }
  | { kind: 'failed'; message: string };

/** deleteAgentPath is the management API path that removes one agent. */
export function deleteAgentPath(agentName: string): string {
  return `/agents/${encodeURIComponent(agentName)}`;
}

/**
 * confirmsAgentName reports whether the typed confirmation matches the agent
 * exactly. Surrounding whitespace is forgiven because it is invisible in the
 * input; nothing else is, so the confirmation cannot be satisfied by a prefix,
 * a different case, or another agent's name.
 */
export function confirmsAgentName(typed: string, agentName: string | null): boolean {
  if (agentName === null || agentName === '') {
    return false;
  }
  return typed.trim() === agentName;
}

function countLabel(count: unknown, noun: string): string {
  const safe = typeof count === 'number' && Number.isFinite(count) ? count : 0;
  return `${safe} ${noun}${safe === 1 ? '' : 's'}`;
}

/**
 * agentDeleteOutcome describes a successful delete using the counts the server
 * reported, and states the lifecycle contract: the agent is not blocked, so its
 * next request brings it back with a fresh budget.
 */
export function agentDeleteOutcome(
  agentName: string,
  response?: AgentDeleteResponse
): AgentDeleteOutcome {
  const removed = response?.removed;
  if (!removed) {
    return { kind: 'deleted', message: `Deleted agent "${agentName}".` };
  }
  return {
    kind: 'deleted',
    message:
      `Deleted agent "${agentName}" and ${countLabel(removed.cost_records, 'cost record')}, ` +
      `${countLabel(removed.alerts, 'alert')}. ` +
      'It is recreated with a fresh budget on its next request.'
  };
}

/**
 * errorStatus reads the HTTP status off a rejection. It matches on the shape
 * rather than on ApiError so this module stays free of runtime imports, which
 * is what lets it be unit tested and imported from a component alike.
 */
function errorStatus(error: unknown): number | null {
  if (typeof error !== 'object' || error === null || !('status' in error)) {
    return null;
  }
  const status = (error as { status: unknown }).status;
  return typeof status === 'number' ? status : null;
}

/**
 * agentDeleteErrorOutcome classifies a failed delete. Every status other than
 * 404 keeps the operator in the dialog with the server's own explanation, which
 * is what makes the 409 protected-agent case readable ("remove it from the
 * config instead") instead of a bare status code.
 */
export function agentDeleteErrorOutcome(agentName: string, error: unknown): AgentDeleteOutcome {
  if (errorStatus(error) === 404) {
    return {
      kind: 'already_deleted',
      message: `Agent "${agentName}" was already deleted.`
    };
  }
  if (error instanceof Error && error.message !== '') {
    return { kind: 'failed', message: error.message };
  }
  return { kind: 'failed', message: `Failed to delete agent "${agentName}".` };
}

/**
 * agentDeleteClosesDialog reports whether an outcome should dismiss the confirm
 * dialog. Both terminal outcomes do; a failure keeps it open so the message is
 * attached to the action that produced it and can be retried.
 */
export function agentDeleteClosesDialog(outcome: AgentDeleteOutcome): boolean {
  return outcome.kind !== 'failed';
}
