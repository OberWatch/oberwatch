/**
 * Source contracts for deleting an agent from the Agents page.
 *
 * These cover only what the rendered markup cannot show. The dialog's own
 * markup and disabled states are asserted in agent-delete.render.test.mjs, and
 * the outcome of a delete request is asserted in src/lib/agentDelete.test.ts;
 * what is left is the wiring: the row action opens the dialog instead of
 * deleting, the keyboard contract exists, and the page routes the request and
 * its outcome through the shared helpers rather than re-deriving them.
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

function read(relativePath) {
  return readFileSync(new URL(relativePath, import.meta.url), 'utf8');
}

const page = read('../src/routes/agents/+page.svelte');
const dialog = read('../src/lib/components/AgentDeleteDialog.svelte');
const types = read('../src/lib/types.ts');
const index = read('../src/lib/components/index.ts');
const sse = read('../src/lib/sse.ts');
const overview = read('../src/routes/+page.svelte');

test('the Agents page offers a delete action that opens the shared dialog', () => {
  assert.match(index, /AgentDeleteDialog/, 'components/index.ts must export AgentDeleteDialog');
  assert.match(page, /AgentDeleteDialog/, 'Agents page must render AgentDeleteDialog');
  assert.match(page, /aria-label=\{`Delete agent \$\{row\.name\}`\}/, 'the row action must name the agent for screen readers');
  assert.match(page, /openDeleteDialog\(row\.name\)/, 'the row action must open the dialog instead of deleting directly');
  assert.doesNotMatch(page, /confirm\([^)]*[Dd]elete/, 'delete must not rely on window.confirm');
});

test('the dialog is operable and escapable from the keyboard', () => {
  assert.match(dialog, /event\.key === 'Escape'/, 'Escape must close the dialog');
  assert.match(dialog, /event\.key === 'Tab'/, 'Tab must be handled so focus cannot leave a modal dialog');
  assert.match(dialog, /dialog\?\.focus\(\)/, 'focus must move to the dialog when it opens');
  assert.match(dialog, /return \(\) => opener\?\.focus\?\.\(\)/, 'focus must return to the opener when the dialog closes');
  assert.match(
    dialog,
    /void agentName;[\s\S]{0,80}typedName = ''/,
    'switching target agents must clear a confirmation typed for the previous one'
  );
});

test('the dialog and the page share one confirmation rule', () => {
  assert.match(
    dialog,
    /import \{ confirmsAgentName \} from '\$lib\/agentDelete'/,
    'the dialog must use the tested confirmation rule rather than its own comparison'
  );
  assert.match(dialog, /confirmsAgentName\(typedName, agentName\)/, 'the confirm gate must call it');
  assert.doesNotMatch(
    dialog,
    /typedName\.trim\(\) === agentName/,
    'the comparison must not be duplicated inline where it could drift'
  );
});

test('the page routes the request and its outcome through the tested helpers', () => {
  assert.match(types, /export interface AgentDeleteResponse/, 'types.ts must type the delete response');
  assert.match(
    page,
    /fetchJSON<AgentDeleteResponse>\(deleteAgentPath\(agentName\),\s*\{\s*method:\s*'DELETE'/,
    'the page must send DELETE to the shared, encoded agent path'
  );
  assert.match(page, /agentDeleteOutcome\(agentName, result\)/, 'a success must be described by the shared helper');
  assert.match(page, /agentDeleteErrorOutcome\(agentName, err\)/, 'a failure must be classified by the shared helper');
  assert.match(page, /agentDeleteClosesDialog\(outcome\)/, 'whether the dialog closes must come from the outcome');
  assert.match(page, /async function deleteAgent[\s\S]*?await loadAgents\(\);/, 'the list must reload after a delete');
  assert.doesNotMatch(
    page,
    /err\.status === 404/,
    'status handling belongs in agentDelete.ts where it is tested, not inline in the page'
  );
});

test('a delete elsewhere reaches the pages that show agent spend', () => {
  // The server publishes agent_deleted; an event nobody listens for leaves an
  // open Overview showing an agent and a total that no longer exist.
  assert.match(types, /export interface AgentDeletedEvent/, 'the event must be part of the typed surface');
  assert.match(sse, /addEventListener\('agent_deleted'/, 'the stream client must subscribe to it');
  assert.match(
    overview,
    /eventName === 'agent_deleted'/,
    'Overview already reloads on agent lifecycle events and must reload on this one'
  );
});
