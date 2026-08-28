/**
 * Contract checks for the sidebar upgrade action (Issue #86).
 *
 * The decisions are unit tested in src/lib/upgrade.test.ts. What these checks
 * defend is the page wiring and the things that are only true of the rendered
 * markup: that the action is a real, focusable button next to the version, that
 * it appears only for a newer supported stable release, that an unsupported
 * installation gets a fallback instruction instead, that the confirmation states
 * the restart and rollback expectations, and that the request the page sends
 * cannot carry a version, tag, URL or path.
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

import { upgradeButtonLabel, upgradeConfirmationLines, upgradeView } from '../src/lib/upgrade.ts';

function read(relativePath) {
  return readFileSync(new URL(relativePath, import.meta.url), 'utf8');
}

const layout = read('../src/routes/+layout.svelte');
const types = read('../src/lib/types.ts');

function functionBody(source, name) {
  const match = source.match(new RegExp(`(?:async )?function ${name}\\s*\\([^)]*\\)[^{]*\\{[\\s\\S]*?\\n  \\}`));
  assert.ok(match, `the layout must define ${name}`);
  return match[0];
}

test('the upgrade response types match what the API sends', () => {
  const statusType = types.match(/export interface UpgradeStatusResponse\s*\{([\s\S]*?)\n\}/);
  assert.ok(statusType, 'UpgradeStatusResponse must be exported');
  for (const field of [
    'current_version',
    'latest_version',
    'update_available',
    'checked_at',
    'check_error',
    'supported',
    'unsupported_reason',
    'fallback',
    'in_progress',
    'last_result'
  ]) {
    assert.match(statusType[1], new RegExp(`\\b${field}\\??:`), `UpgradeStatusResponse must carry ${field}`);
  }

  const resultStatus = types.match(/export type UpgradeResultStatus\s*=([\s\S]*?);/);
  assert.ok(resultStatus, 'UpgradeResultStatus must be exported');
  for (const value of ['succeeded', 'restart_required', 'failed']) {
    assert.match(resultStatus[1], new RegExp(`'${value}'`), `UpgradeResultStatus must include ${value}`);
  }
});

test('the layout decides what to show through the tested helpers', () => {
  assert.match(
    layout,
    /import\s*\{[^}]*\bupgradeView\b[^}]*\}\s*from\s*'\$lib\/upgrade'/,
    'the footer must decide its state with upgradeView'
  );
  assert.match(
    layout,
    /import\s*\{[^}]*\bupgradeConfirmationLines\b[^}]*\}\s*from\s*'\$lib\/upgrade'/,
    'the confirmation copy must come from the tested helper'
  );
  assert.match(
    layout,
    /import\s*\{[^}]*\bupgradePollDecision\b[^}]*\}\s*from\s*'\$lib\/upgrade'/,
    'waiting for the restart must use the tested decision'
  );
  assert.doesNotMatch(
    layout,
    /function upgradeView|function upgradeButtonLabel|function upgradeResultNote/,
    'the layout must not keep a private copy of the upgrade decisions'
  );
  assert.doesNotMatch(
    layout,
    /update_available\s*&&/,
    'the footer must not re-derive availability; upgradeView owns that rule'
  );
});

test('the upgrade request carries no target of any kind', () => {
  const confirm = functionBody(layout, 'confirmUpgrade');

  assert.match(confirm, /fetchJSON<UpgradeStartResponse>\('\/upgrade',\s*\{\s*method:\s*'POST'\s*\}\)/, 'the POST must be sent with no body');
  assert.doesNotMatch(confirm, /body:/, 'the upgrade request must never carry a body');
  assert.doesNotMatch(confirm, /JSON\.stringify/, 'nothing may be serialised into the upgrade request');
  for (const name of ['tag', 'version', 'url', 'path', 'archive', 'command']) {
    assert.doesNotMatch(
      confirm,
      new RegExp(`\\b${name}\\s*:\\s*`),
      `the upgrade request must not send a ${name}`
    );
  }

  // The tag the page then waits for is the one the server answered with, not one
  // the page chose.
  assert.match(confirm, /upgradeTarget = started\.tag/, 'the target must come from the server response');
  assert.match(confirm, /waitForRestart\(started\.tag\)/);
});

test('only a real HTTP refusal is reported as a failed upgrade', () => {
  const confirm = functionBody(layout, 'confirmUpgrade');

  assert.match(
    confirm,
    /if \(!\(error instanceof ApiError\)\) \{[\s\S]*?upgradePhase = 'waiting';[\s\S]*?waitForRestart\(/,
    'a dropped connection is ambiguous and must be waited on, not reported as a failure'
  );
  assert.match(
    confirm,
    /upgradeError = upgradeFailureMessage\(error\.details\?\.error\?\.code, error\.message\)/,
    'a refusal must be reported through the tested message helper'
  );
  assert.doesNotMatch(
    confirm,
    /upgradePhase = 'failed';[\s\S]*?upgradePhase = 'failed'/,
    'there must be exactly one path that reports a failure'
  );
});

test('a failed status read never blanks the footer or invents a failure', () => {
  const load = functionBody(layout, 'loadUpgradeStatus');

  assert.doesNotMatch(load, /upgradeStatus = null/, 'a failed read must keep the last known status on screen');
  assert.doesNotMatch(load, /upgradeError\s*=/, 'a failed read is not a failed upgrade');
  assert.match(load, /catch\s*\{\s*return null;?\s*\}/, 'a failed read must be reported as "no answer", not as an outcome');
});

test('waiting for the restart is bounded and reports a timeout honestly', () => {
  const wait = functionBody(layout, 'waitForRestart');

  assert.match(wait, /UPGRADE_POLL_INTERVAL_MS/, 'the poll cadence must come from the tested constant');
  assert.match(wait, /upgradePollDecision\(/);
  assert.match(wait, /upgradeWaitTimedOut = decision === 'timeout'/);
  assert.doesNotMatch(wait, /upgradeStatus = /, 'the wait must not write status directly; loadUpgradeStatus owns that');
});

// The layout renders its sidebar only for an authenticated session and fills its
// state on mount, which server rendering does not run. The footer markup is
// therefore asserted against the source, and the state-dependent branches are
// asserted through upgradeView, which is what selects them.

test('the action is a real button next to the version, with a visible focus ring', () => {
  const footer = layout.match(/<div class="border-t border-border-default pt-4">[\s\S]*?<\/div>\s*<\/aside>/);
  assert.ok(footer, 'the sidebar must still have a footer below the nav');

  const section = footer[0];
  assert.match(section, /Logout/, 'the footer must still hold the Logout action');
  assert.match(
    section,
    /<span class="text-xs text-text-secondary">\{displayVersion\}<\/span>[\s\S]{0,2000}?<button/,
    'the upgrade action must sit next to the running version'
  );

  const actionButton = section.match(/<button[^>]*onclick=\{beginUpgrade\}[\s\S]*?<\/button>/);
  assert.ok(actionButton, 'the upgrade action must be a button that starts the confirmation');
  assert.match(actionButton[0], /type="button"/, 'the action must be a real button, so it is keyboard reachable');
  assert.match(actionButton[0], /focus-visible:outline/, 'the action must show a visible focus ring');
  assert.match(actionButton[0], /\{upgrade\.action\}/, 'the label must come from upgradeView');

  assert.doesNotMatch(section, /<a [^>]*onclick=\{beginUpgrade\}/, 'the action must not be a link pretending to be a button');
  assert.doesNotMatch(section, /<div [^>]*onclick=\{beginUpgrade\}/, 'the action must not be a div pretending to be a button');
});

test('the action is only rendered for a newer supported release', () => {
  const section = layout.match(/\{#if upgradeBusy\}[\s\S]*?\{\/if\}/);
  assert.ok(section, 'the footer must branch on the upgrade state');
  assert.match(
    section[0],
    /\{:else if upgrade\.kind === 'available'[^}]*\}\s*<button/,
    'the button must be gated on the available state alone'
  );

  // Every state that is not "available" must resolve to something other than the
  // action, decided by the tested helper rather than by the markup.
  const notAvailable = [
    null,
    { current_version: 'v0.1.3', update_available: false, supported: true, in_progress: false, checked_at: '2026-08-28T12:00:00Z' },
    { current_version: 'v0.1.3', update_available: false, supported: true, in_progress: false },
    { current_version: 'v0.1.3', update_available: false, supported: true, in_progress: false, check_error: 'timeout' },
    {
      current_version: 'v0.1.3',
      latest_version: 'v0.1.4',
      update_available: true,
      supported: false,
      in_progress: false,
      checked_at: '2026-08-28T12:00:00Z',
      fallback: 'Re-run the installer'
    }
  ];
  for (const status of notAvailable) {
    assert.notEqual(upgradeView(status).kind, 'available', `${JSON.stringify(status)} must not offer the action`);
    assert.equal(upgradeView(status).action, null);
  }
});

test('an unsupported installation renders the fallback instruction, not an action', () => {
  assert.match(
    layout,
    /\{#if upgrade\.kind === 'unsupported'\}[\s\S]{0,200}?role="status"[\s\S]{0,80}?\{upgrade\.note\}/,
    'the fallback must render in an accessible status region'
  );

  const view = upgradeView({
    current_version: 'v0.1.3',
    latest_version: 'v0.1.4',
    update_available: true,
    supported: false,
    in_progress: false,
    checked_at: '2026-08-28T12:00:00Z',
    fallback: 'Pull the new image and recreate the container: docker pull ghcr.io/oberwatch/oberwatch:latest'
  });
  assert.equal(view.action, null);
  assert.match(view.note, /docker pull/);
});

test('the confirmation step states the restart, downtime, data safety and rollback', () => {
  const panel = layout.match(/\{#if upgradePhase === 'confirming'\}[\s\S]*?\{\/if\}/);
  assert.ok(panel, 'confirming an upgrade must render a confirmation panel');

  assert.match(panel[0], /\{#each upgradeConfirmation as line\}/, 'the panel must render the tested confirmation lines');
  assert.match(panel[0], /onclick=\{confirmUpgrade\}/, 'the panel must have an explicit confirm action');
  assert.match(panel[0], /onclick=\{cancelUpgrade\}/, 'the panel must be cancellable');
  assert.equal(
    (panel[0].match(/type="button"/g) ?? []).length,
    2,
    'both confirmation controls must be real buttons'
  );
  assert.equal(
    (panel[0].match(/focus-visible:outline\s/g) ?? []).length,
    2,
    'both confirmation controls must show a visible focus ring'
  );

  const lines = upgradeConfirmationLines('v0.1.4').join(' ');
  assert.match(lines, /restarts/);
  assert.match(lines, /unavailable/);
  assert.match(lines, /not touched/);
  assert.match(lines, /rolled back/);
});

test('the footer has a loading state, a busy state and an accessible result region', () => {
  assert.match(
    layout,
    /\{#if upgradeBusy\}[\s\S]{0,400}?role="status"[\s\S]{0,120}?aria-busy="true"/,
    'the in-flight state must be announced'
  );
  assert.match(
    layout,
    /\{:else if upgrade\.kind === 'checking'\}/,
    'the footer must have a loading state before the first check completes'
  );
  assert.match(
    layout,
    /\{:else if upgrade\.kind === 'unavailable'\}/,
    'the footer must have an unavailable state for a failed check'
  );
  assert.match(layout, /aria-live="polite"/, 'outcome changes must be announced');
  assert.match(layout, /\{upgradeResult\.text\}/, 'the recorded outcome must be shown');
  assert.match(layout, /\{upgradeError\}/, 'a failure must be shown');
  assert.match(layout, /upgradeWaitTimedOut/, 'a restart that never came back must be reported honestly');
});

test('the label the button shows is the checked release', () => {
  assert.equal(upgradeButtonLabel('v0.1.4'), 'Upgrade to v0.1.4');
  assert.equal(
    upgradeView({
      current_version: 'v0.1.3',
      latest_version: 'v0.1.4',
      update_available: true,
      supported: true,
      in_progress: false,
      checked_at: '2026-08-28T12:00:00Z'
    }).action,
    'Upgrade to v0.1.4'
  );
});

test('the upgrade status is only read for an authenticated session', () => {
  assert.match(
    layout,
    /if \(authStatus\?\.authenticated\) \{\s*await loadUpgradeStatus\(\);/,
    'the authenticated upgrade status must not be requested on the login or setup screens'
  );
});
