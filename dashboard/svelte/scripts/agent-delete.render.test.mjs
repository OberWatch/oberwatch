/**
 * Rendering tests for the agent delete confirmation dialog.
 *
 * Deleting an agent throws away its spend history, so the parts that make that
 * safe are asserted against markup produced by the real Svelte compiler and
 * server renderer rather than against source text: the destructive button is
 * genuinely disabled until the name is typed, the modal genuinely carries the
 * semantics screen readers need, the consequences are genuinely on screen, and
 * an in-flight delete genuinely cannot be submitted twice.
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';

import { render } from './lib/render-svelte.mjs';

const DIALOG = 'lib/components/AgentDeleteDialog.svelte';
const AGENT = 'scratch-agent';

/** openTags returns every opening tag for an element, attributes included. */
function openTags(markup, name) {
  return markup.match(new RegExp(`<${name}\\b[^>]*>`, 'g')) ?? [];
}

/** onlyTag returns the single opening tag matching a predicate. */
function onlyTag(markup, name, predicate, label) {
  const matches = openTags(markup, name).filter(predicate);
  assert.equal(matches.length, 1, `expected exactly one ${label}, found ${matches.length}`);
  return matches[0];
}

function overlay(markup) {
  return onlyTag(markup, 'button', (tag) => tag.includes('aria-label="Cancel deleting agent"'), 'overlay');
}

function submitButton(markup) {
  return onlyTag(markup, 'button', (tag) => tag.includes('type="submit"'), 'submit button');
}

function cancelButton(markup) {
  return onlyTag(
    markup,
    'button',
    (tag) => tag.includes('type="button"') && !tag.includes('aria-label='),
    'cancel button'
  );
}

function confirmInput(markup) {
  return onlyTag(markup, 'input', () => true, 'confirmation input');
}

test('the dialog renders nothing until it is opened for a named agent', async () => {
  const cases = [
    { name: 'closed', props: { open: false, agentName: AGENT } },
    { name: 'open with no target', props: { open: true, agentName: null } },
    { name: 'closed with no target', props: { open: false, agentName: null } }
  ];

  for (const { name, props } of cases) {
    const markup = await render(DIALOG, props);
    assert.doesNotMatch(markup, /role="dialog"/, `${name} must not render the dialog`);
    assert.equal(openTags(markup, 'button').length, 0, `${name} must not render any control`);
  }
});

test('the open dialog carries the modal semantics assistive tech needs', async () => {
  const markup = await render(DIALOG, { open: true, agentName: AGENT });

  const dialog = onlyTag(markup, 'div', (tag) => tag.includes('role="dialog"'), 'dialog');
  assert.match(dialog, /aria-modal="true"/, 'the dialog must be marked modal');
  assert.match(dialog, /tabindex="-1"/, 'the dialog must be focusable so focus can move into it');

  // The labelling and describing ids must resolve to elements that exist,
  // otherwise the dialog announces as unnamed.
  for (const attribute of ['aria-labelledby', 'aria-describedby']) {
    const id = new RegExp(`${attribute}="([^"]+)"`).exec(dialog)?.[1];
    assert.ok(id, `the dialog must set ${attribute}`);
    assert.match(markup, new RegExp(`id="${id}"`), `${attribute}="${id}" must point at a rendered element`);
  }
});

test('the destructive button is disabled until the exact name is typed', async () => {
  const markup = await render(DIALOG, { open: true, agentName: AGENT });

  assert.match(
    submitButton(markup),
    /\sdisabled(=|\s|>)/,
    'an untouched dialog must not be able to delete: Enter or a click has to be refused'
  );
  assert.match(confirmInput(markup), /value=""/, 'the confirmation box starts empty');
  assert.match(confirmInput(markup), new RegExp(`placeholder="${AGENT}"`), 'the box names what to type');
  assert.doesNotMatch(confirmInput(markup), /\sdisabled(=|\s|>)/, 'the box must be usable');
  assert.match(markup, /matches exactly/, 'the rule for enabling deletion must be stated');
});

test('the dialog names the agent and states what deleting it does', async () => {
  const markup = await render(DIALOG, { open: true, agentName: AGENT });

  const heading = /<h2[^>]*>([\s\S]*?)<\/h2>/.exec(markup)?.[1] ?? '';
  assert.match(heading, new RegExp(AGENT), 'the heading must name the agent being deleted');

  for (const [what, pattern] of [
    ['the agent record and budget state', /budget state/],
    ['cost records', /cost records/],
    ['alerts', /alerts/],
    ['task budgets are kept', /Task budgets[\s\S]*not changed/],
    ['other agents are untouched', /other agents/],
    ['settings are untouched', /settings/],
    ['the agent is not blocked', /not blocked/],
    ['the next request recreates it', /recreates it/],
    ['it comes back with a zero spend', /\$0/]
  ]) {
    assert.match(markup, pattern, `the dialog must state that ${what}`);
  }
});

test('an in-flight delete is announced and cannot be submitted or dismissed again', async () => {
  const markup = await render(DIALOG, { open: true, agentName: AGENT, busy: true });

  assert.match(submitButton(markup), /aria-busy="true"/, 'the in-flight state must be exposed');
  assert.match(markup, /Deleting/, 'the button must say what is happening');
  assert.match(submitButton(markup), /\sdisabled(=|\s|>)/, 'a second submit must be refused');
  assert.match(cancelButton(markup), /\sdisabled(=|\s|>)/, 'cancel must be refused mid-request');
  assert.match(overlay(markup), /\sdisabled(=|\s|>)/, 'clicking the backdrop must be refused mid-request');
  assert.match(confirmInput(markup), /\sdisabled(=|\s|>)/, 'the confirmation box must be frozen');

  const idle = await render(DIALOG, { open: true, agentName: AGENT });
  assert.match(idle, /aria-busy="false"/, 'an idle dialog must not claim to be busy');
  assert.doesNotMatch(cancelButton(idle), /\sdisabled(=|\s|>)/, 'cancel must work when idle');
  assert.doesNotMatch(overlay(idle), /\sdisabled(=|\s|>)/, 'the backdrop must dismiss when idle');
});

test('a failed delete is announced next to the action that caused it', async () => {
  const message = 'agent is defined in the configuration file and cannot be deleted from the dashboard';
  const markup = await render(DIALOG, { open: true, agentName: AGENT, errorMessage: message });

  const alert = /<div[^>]*role="alert"[^>]*>([\s\S]*?)<\/div>/.exec(markup);
  assert.ok(alert, 'the error must be in a live region so it is announced');
  assert.match(alert[1], new RegExp(message), 'the server explanation must be shown verbatim');

  const idle = await render(DIALOG, { open: true, agentName: AGENT });
  assert.doesNotMatch(idle, /role="alert"/, 'no live region until there is something to announce');
});

test('the agent name is escaped everywhere it is echoed', async () => {
  // The API rejects names outside [A-Za-z0-9_-], so this is a second line of
  // defence: whatever reaches the dialog is rendered as text, not markup.
  const markup = await render(DIALOG, { open: true, agentName: '<img src=x onerror=y>' });

  assert.doesNotMatch(markup, /<img/, 'the name must never be interpolated as markup');
  assert.match(markup, /&lt;img/, 'the name must be escaped where it is shown');
});
