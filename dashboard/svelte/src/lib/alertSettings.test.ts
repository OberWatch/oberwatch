import { test } from 'node:test';
import assert from 'node:assert/strict';

import {
  alertSettingsToFormState,
  buildAlertSettingsPatch,
  clearSecretPatch,
  formatRecipients,
  hasAlertSettingsChanges,
  parseRecipients,
  secretStatusLabel
} from './alertSettings.ts';
import type { AlertSettingsResponse } from './types.ts';

function settings(overrides: Partial<AlertSettingsResponse> = {}): AlertSettingsResponse {
  return {
    smtp_host: 'smtp.example.com',
    smtp_port: 587,
    smtp_user: 'alerts',
    smtp_from: 'alerts@example.com',
    smtp_to: ['ops@example.com', 'oncall@example.com'],
    smtp_enabled: true,
    smtp_password_is_set: true,
    webhook_url_is_set: false,
    slack_webhook_url_is_set: false,
    live_applied: true,
    ...overrides
  };
}

test('parseRecipients trims, splits, and drops empty entries', () => {
  assert.deepEqual(parseRecipients('a@x.com, b@x.com'), ['a@x.com', 'b@x.com']);
  assert.deepEqual(parseRecipients(' a@x.com ,, b@x.com ,'), ['a@x.com', 'b@x.com']);
  assert.deepEqual(parseRecipients(''), []);
  assert.deepEqual(parseRecipients('   '), []);
});

test('formatRecipients is the inverse of parseRecipients for seeding the field', () => {
  assert.equal(formatRecipients(['a@x.com', 'b@x.com']), 'a@x.com, b@x.com');
  assert.equal(formatRecipients([]), '');
});

test('alertSettingsToFormState never carries a secret value, only the fields the API actually returns', () => {
  const form = alertSettingsToFormState(settings());
  assert.equal(form.smtpPasswordInput, '');
  assert.equal(form.webhookInput, '');
  assert.equal(form.slackInput, '');
  assert.equal(form.smtpHost, 'smtp.example.com');
  assert.equal(form.smtpPort, '587');
  assert.equal(form.smtpToText, 'ops@example.com, oncall@example.com');
  assert.equal(form.smtpEnabled, true);
});

test('secretStatusLabel reports a status without ever showing the secret', () => {
  assert.equal(secretStatusLabel(true), 'Configured');
  assert.equal(secretStatusLabel(false), 'Not configured');
});

test('buildAlertSettingsPatch is empty when nothing changed', () => {
  const original = settings();
  const form = alertSettingsToFormState(original);
  assert.deepEqual(buildAlertSettingsPatch(original, form), {});
});

test('buildAlertSettingsPatch sends only the fields that changed', () => {
  const original = settings();
  const form = alertSettingsToFormState(original);
  form.smtpHost = 'smtp2.example.com';
  assert.deepEqual(buildAlertSettingsPatch(original, form), { smtp_host: 'smtp2.example.com' });
});

test('buildAlertSettingsPatch parses the port and ignores unparseable text', () => {
  const original = settings();
  const changed = alertSettingsToFormState(original);
  changed.smtpPort = '2525';
  assert.deepEqual(buildAlertSettingsPatch(original, changed), { smtp_port: 2525 });

  const bad = alertSettingsToFormState(original);
  bad.smtpPort = 'not-a-port';
  assert.deepEqual(buildAlertSettingsPatch(original, bad), {});
});

test('buildAlertSettingsPatch diffs the recipient list, ignoring incidental spacing', () => {
  const original = settings();
  const same = alertSettingsToFormState(original);
  same.smtpToText = 'ops@example.com,   oncall@example.com';
  assert.deepEqual(buildAlertSettingsPatch(original, same), {});

  const changed = alertSettingsToFormState(original);
  changed.smtpToText = 'ops@example.com';
  assert.deepEqual(buildAlertSettingsPatch(original, changed), { smtp_to: ['ops@example.com'] });
});

test('buildAlertSettingsPatch omits a secret left blank, so an unedited save can never clear it', () => {
  const original = settings();
  const form = alertSettingsToFormState(original);
  form.smtpPasswordInput = '   ';
  form.webhookInput = '';
  form.slackInput = '';
  assert.deepEqual(buildAlertSettingsPatch(original, form), {});
});

test('buildAlertSettingsPatch includes a secret only when the user actually typed a new one', () => {
  const original = settings();
  const form = alertSettingsToFormState(original);
  form.smtpPasswordInput = 'new-secret';
  form.webhookInput = 'https://hooks.example.com/a';
  form.slackInput = 'https://hooks.slack.com/b';
  assert.deepEqual(buildAlertSettingsPatch(original, form), {
    smtp_password: 'new-secret',
    webhook_url: 'https://hooks.example.com/a',
    slack_webhook_url: 'https://hooks.slack.com/b'
  });
});

test('buildAlertSettingsPatch combines every changed field in one patch', () => {
  const original = settings();
  const form = alertSettingsToFormState(original);
  form.smtpEnabled = false;
  form.smtpUser = 'new-user';
  form.smtpFrom = 'new-from@example.com';
  assert.deepEqual(buildAlertSettingsPatch(original, form), {
    smtp_enabled: false,
    smtp_user: 'new-user',
    smtp_from: 'new-from@example.com'
  });
});

test('hasAlertSettingsChanges reflects whether the patch is empty', () => {
  assert.equal(hasAlertSettingsChanges({}), false);
  assert.equal(hasAlertSettingsChanges({ smtp_host: 'x' }), true);
});

test('clearSecretPatch is the only place an empty string is sent for a secret', () => {
  assert.deepEqual(clearSecretPatch('smtp_password'), { smtp_password: '' });
  assert.deepEqual(clearSecretPatch('webhook_url'), { webhook_url: '' });
  assert.deepEqual(clearSecretPatch('slack_webhook_url'), { slack_webhook_url: '' });
});
