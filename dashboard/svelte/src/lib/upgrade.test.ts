import { test } from 'node:test';
import assert from 'node:assert/strict';

import {
  UPGRADE_POLL_INTERVAL_MS,
  UPGRADE_POLL_TIMEOUT_MS,
  upgradeButtonLabel,
  upgradeConfirmationLines,
  upgradeFailureMessage,
  upgradePollDecision,
  upgradeResultNote,
  upgradeView
} from './upgrade.ts';
import type { UpgradeResult, UpgradeStatusResponse } from './types.ts';

function status(overrides: Partial<UpgradeStatusResponse> = {}): UpgradeStatusResponse {
  return {
    current_version: 'v0.1.3',
    update_available: false,
    supported: true,
    in_progress: false,
    checked_at: '2026-08-28T12:00:00Z',
    ...overrides
  };
}

test('nothing is claimed before the first release check finishes', () => {
  assert.deepEqual(upgradeView(null), {
    kind: 'checking',
    action: null,
    note: 'Checking for updates'
  });

  const pending = upgradeView(status({ checked_at: undefined }));
  assert.equal(pending.kind, 'checking');
  assert.equal(pending.action, null, 'no action may be offered before a check completes');
});

test('an available newer stable release becomes the upgrade action', () => {
  const view = upgradeView(status({ update_available: true, latest_version: 'v0.1.4' }));
  assert.equal(view.kind, 'available');
  assert.equal(view.action, 'Upgrade to v0.1.4');
  assert.equal(view.note, null);
});

test('no action is offered when there is nothing newer to install', () => {
  const cases: Partial<UpgradeStatusResponse>[] = [
    { update_available: false, latest_version: 'v0.1.3' },
    { update_available: false, latest_version: 'v0.1.2' },
    { update_available: false },
    { update_available: true, latest_version: '' },
    { update_available: true, latest_version: '   ' }
  ];

  for (const overrides of cases) {
    const view = upgradeView(status(overrides));
    assert.equal(view.action, null, `an action was offered for ${JSON.stringify(overrides)}`);
    assert.notEqual(view.kind, 'available', `${JSON.stringify(overrides)} was treated as available`);
  }
});

test('a failed release check is reported, never as up to date', () => {
  const view = upgradeView(status({ check_error: 'release check unavailable: timeout' }));
  assert.equal(view.kind, 'unavailable');
  assert.equal(view.action, null);
  assert.equal(view.note, 'Update check unavailable');
});

test('a release observed before a later check failed is still reported', () => {
  const view = upgradeView(
    status({ update_available: true, latest_version: 'v0.1.4', check_error: 'release check unavailable: timeout' })
  );
  assert.equal(view.kind, 'available', 'a known newer release must not be hidden by a later failed check');
  assert.equal(view.action, 'Upgrade to v0.1.4');
});

test('an unsupported installation gets the fallback instruction instead of a button', () => {
  const view = upgradeView(
    status({
      update_available: true,
      latest_version: 'v0.1.4',
      supported: false,
      unsupported_reason: 'running in a container (/.dockerenv)',
      fallback: 'Pull the new image and recreate the container: docker pull ghcr.io/oberwatch/oberwatch:latest'
    })
  );

  assert.equal(view.kind, 'unsupported');
  assert.equal(view.action, null, 'an unsupported installation must never be shown an action that cannot work');
  assert.match(view.note ?? '', /v0\.1\.4 is available/);
  assert.match(view.note ?? '', /docker pull/);
});

test('an unsupported installation without a fallback still says so', () => {
  const view = upgradeView(status({ update_available: true, latest_version: 'v0.1.4', supported: false }));
  assert.equal(view.kind, 'unsupported');
  assert.equal(view.action, null);
  assert.match(view.note ?? '', /not available for this installation/);
});

test('the action label carries exactly one v prefix', () => {
  assert.equal(upgradeButtonLabel('v0.1.4'), 'Upgrade to v0.1.4');
  assert.equal(upgradeButtonLabel('0.1.4'), 'Upgrade to v0.1.4');
  assert.equal(upgradeButtonLabel('  v1.10.0  '), 'Upgrade to v1.10.0');
});

test('the confirmation states restart, downtime, data safety and rollback', () => {
  const lines = upgradeConfirmationLines('v0.1.4');
  const joined = lines.join(' ');

  assert.match(joined, /v0\.1\.4/);
  assert.match(joined, /checksum/, 'the confirmation must say the release is verified');
  assert.match(joined, /restarts/, 'the confirmation must say the service restarts');
  assert.match(joined, /unavailable/, 'the confirmation must say there is downtime');
  assert.match(joined, /configuration file and database are not touched/i);
  assert.match(joined, /rolled back/, 'the confirmation must say the upgrade can be undone');
  assert.equal(upgradeConfirmationLines('0.1.4')[0].includes('v0.1.4'), true);
});

test('every refusal code has a message an operator can act on', () => {
  const codes = [
    'upgrade_unsupported',
    'upgrade_not_available',
    'upgrade_in_progress',
    'upgrade_verification_failed',
    'upgrade_source_unavailable'
  ];

  for (const code of codes) {
    const message = upgradeFailureMessage(code, 'raw server detail');
    assert.notEqual(message.trim(), '', `${code} has no message`);
    assert.doesNotMatch(message, /raw server detail/, `${code} should have its own wording`);
  }

  assert.equal(upgradeFailureMessage('upgrade_failed', 'disk full'), 'disk full');
  assert.equal(upgradeFailureMessage(undefined, 'network unreachable'), 'network unreachable');
  assert.equal(upgradeFailureMessage(undefined, '   '), 'The upgrade failed.');
});

test('refusal messages say that nothing was installed', () => {
  for (const code of ['upgrade_unsupported', 'upgrade_verification_failed', 'upgrade_source_unavailable']) {
    assert.match(
      upgradeFailureMessage(code, ''),
      /nothing was installed|Nothing was changed/i,
      `${code} must state that nothing was installed`
    );
  }
});

function result(overrides: Partial<UpgradeResult> = {}): UpgradeResult {
  return {
    status: 'succeeded',
    tag: 'v0.1.4',
    from: 'v0.1.3',
    message: 'Installed v0.1.4 and restarted oberwatch.',
    ...overrides
  };
}

test('a recorded outcome is reported with the right severity', () => {
  assert.equal(upgradeResultNote(null), null);
  assert.equal(upgradeResultNote(undefined), null);

  const success = upgradeResultNote(result());
  assert.equal(success?.tone, 'success');
  assert.match(success?.text ?? '', /Updated to v0\.1\.4/);

  const failed = upgradeResultNote(result({ status: 'failed', message: 'checksum mismatch. Nothing was installed.' }));
  assert.equal(failed?.tone, 'error');
  assert.match(failed?.text ?? '', /failed/);
  assert.match(failed?.text ?? '', /checksum mismatch/);
});

test('an installed-but-not-restarted outcome is not reported as a success', () => {
  const note = upgradeResultNote(
    result({ status: 'restart_required', message: 'Restart it with: sudo systemctl restart oberwatch.' })
  );

  assert.equal(note?.tone, 'warning');
  assert.doesNotMatch(note?.text ?? '', /^Updated to/);
  assert.match(note?.text ?? '', /previous version is still running/);
  assert.match(note?.text ?? '', /systemctl restart oberwatch/);
});

test('waiting for the restart stops on a recorded outcome, not on a failed read', () => {
  assert.equal(
    upgradePollDecision({ status: null, target: 'v0.1.4', elapsedMs: 6_000 }),
    'continue',
    'a failed read during the restart is not an outcome'
  );

  assert.equal(
    upgradePollDecision({
      status: status({ current_version: 'v0.1.3' }),
      target: 'v0.1.4',
      elapsedMs: 6_000
    }),
    'continue'
  );

  assert.equal(
    upgradePollDecision({
      status: status({ current_version: 'v0.1.4' }),
      target: 'v0.1.4',
      elapsedMs: 6_000
    }),
    'done'
  );

  assert.equal(
    upgradePollDecision({
      status: status({ current_version: 'v0.1.3', last_result: result({ status: 'failed' }) }),
      target: 'v0.1.4',
      elapsedMs: 6_000
    }),
    'done',
    'a recorded failure for this release is an outcome too'
  );

  assert.equal(
    upgradePollDecision({
      status: status({ current_version: 'v0.1.3', last_result: result({ tag: 'v0.1.2' }) }),
      target: 'v0.1.4',
      elapsedMs: 6_000
    }),
    'continue',
    'an outcome recorded for another release is not this attempt s'
  );
});

test('waiting for the restart is bounded', () => {
  assert.equal(
    upgradePollDecision({ status: null, target: 'v0.1.4', elapsedMs: UPGRADE_POLL_TIMEOUT_MS }),
    'timeout'
  );
  assert.equal(
    upgradePollDecision({ status: null, target: 'v0.1.4', elapsedMs: UPGRADE_POLL_TIMEOUT_MS - 1 }),
    'continue'
  );
  assert.ok(UPGRADE_POLL_INTERVAL_MS > 0 && UPGRADE_POLL_INTERVAL_MS < UPGRADE_POLL_TIMEOUT_MS);
});
