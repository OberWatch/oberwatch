/**
 * Contract checks for the Settings "Alert delivery" form (Issue #55).
 *
 * The diffing and formatting logic is unit tested in
 * src/lib/alertSettings.test.ts. What these checks defend is the page wiring:
 * that a settings-alert load failure is nonfatal and never erases the
 * password form or other page state, that no secret is ever interpolated
 * into a visible field, and that a save only PATCHes what changed while the
 * dedicated clear controls are the only path that sends an empty secret.
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

function read(relativePath) {
  return readFileSync(new URL(relativePath, import.meta.url), 'utf8');
}

const settings = read('../src/routes/settings/+page.svelte');

function functionBody(source, name) {
  const match = source.match(new RegExp(`(?:async )?function ${name}\\s*\\([^)]*\\)[^{]*\\{[\\s\\S]*?\\n  \\}`));
  assert.ok(match, `Settings must define ${name}`);
  return match[0];
}

test('Settings loads alert delivery settings through the tested helpers', () => {
  assert.match(
    settings,
    /import\s*\{[^}]*\balertSettingsToFormState\b[^}]*\}\s*from\s*'\$lib\/alertSettings'/,
    'Settings must seed its form with the tested helper'
  );
  assert.match(
    settings,
    /import\s*\{[^}]*\bbuildAlertSettingsPatch\b[^}]*\}\s*from\s*'\$lib\/alertSettings'/,
    'Settings must diff its form with the tested helper'
  );
  assert.match(
    settings,
    /import\s*\{[^}]*\bsecretStatusLabel\b[^}]*\}\s*from\s*'\$lib\/alertSettings'/,
    'Settings must label secret fields with the tested helper'
  );
  assert.match(
    settings,
    /import\s*\{[^}]*\bclearSecretPatch\b[^}]*\}\s*from\s*'\$lib\/alertSettings'/,
    'Settings must clear secrets with the tested helper'
  );
});

test('loading alert settings is nonfatal and never erases other page state', () => {
  const loader = functionBody(settings, 'loadAlertSettings');

  assert.doesNotMatch(
    loader,
    /\berrorMessage\s*=/,
    'an alert-settings load failure must not replace the page with ErrorState'
  );
  for (const field of [
    'currentPassword',
    'newPassword',
    'confirmPassword',
    'pricingRows',
    'providerRows'
  ]) {
    assert.doesNotMatch(
      loader,
      new RegExp(`\\b${field}\\s*=`),
      `loading alert settings must not touch ${field}`
    );
  }

  const settingsLoader = functionBody(settings, 'loadSettings');
  assert.doesNotMatch(
    settingsLoader,
    /await loadAlertSettings\(\);[\s\S]*?return;/,
    'a settings-alert failure must not abort the rest of loadSettings'
  );
});

test('no secret is ever interpolated into a visible field', () => {
  for (const secretVar of ['smtpPasswordInput', 'webhookInput', 'slackInput']) {
    assert.doesNotMatch(
      settings,
      new RegExp(`${secretVar}\\s*=\\s*(alertSettings|response|settings)\\.`),
      `${secretVar} must never be assigned from a fetched response`
    );
  }
  assert.doesNotMatch(settings, /console\.(log|debug|info|warn|error)\([^)]*(password|webhook|slack)/i, 'a secret must never be logged');
});

test('secret inputs are write-only password fields, always blank until the user types', () => {
  const passwordFieldPattern =
    /bind:value=\{smtpPasswordInput\}[\s\S]{0,120}?type="password"|type="password"[\s\S]{0,120}?bind:value=\{smtpPasswordInput\}/;
  assert.match(settings, passwordFieldPattern, 'the SMTP password field must be a password input bound to a local write-only variable');

  for (const secretVar of ['webhookInput', 'slackInput']) {
    assert.match(
      settings,
      new RegExp(`bind:value=\\{${secretVar}\\}`),
      `${secretVar} must be bound to an input`
    );
  }
});

test('status text for the three secrets comes from the is_set flags via secretStatusLabel', () => {
  for (const flag of ['smtpPasswordIsSet', 'webhookIsSet', 'slackIsSet']) {
    assert.match(
      settings,
      new RegExp(`secretStatusLabel\\(${flag}\\)`),
      `the status text for ${flag} must come from secretStatusLabel`
    );
  }
  for (const flag of ['smtp_password_is_set', 'webhook_url_is_set', 'slack_webhook_url_is_set']) {
    assert.match(settings, new RegExp(`\\.${flag}\\b`), `the ${flag} flag must be read from the API response`);
  }
});

test('save sends only changed fields via PATCH /settings/alerts', () => {
  assert.match(
    settings,
    /buildAlertSettingsPatch\(/,
    'saving must diff the form before sending anything'
  );
  assert.match(
    settings,
    /fetchJSON[^;]*'\/settings\/alerts'[^;]*method:\s*'PATCH'/s,
    'saving must PATCH /settings/alerts'
  );
  assert.match(
    settings,
    /hasAlertSettingsChanges\(/,
    'saving must check whether there is anything to send'
  );
});

test('the clear controls PATCH an empty string through the dedicated helper, never through the save diff', () => {
  const clearFn = functionBody(settings, 'clearAlertSecret');
  assert.match(clearFn, /clearSecretPatch\(field\)/, 'clearAlertSecret must build its PATCH with clearSecretPatch');
  assert.doesNotMatch(
    clearFn,
    /buildAlertSettingsPatch\(/,
    'clearing a secret must never go through the save diff'
  );

  const calls = [...settings.matchAll(/clearAlertSecret\('(\w+)'\)/g)].map((m) => m[1]);
  for (const field of ['smtp_password', 'webhook_url', 'slack_webhook_url']) {
    assert.ok(calls.includes(field), `a clear control must call clearAlertSecret('${field}')`);
  }
});

test('the form states that changes apply immediately without a restart', () => {
  assert.match(
    settings,
    /appl(y|ies)[\s\S]{0,40}?immediately/i,
    'the page must tell the user changes apply immediately'
  );
  assert.doesNotMatch(
    settings,
    /restart required|edit(ing)? the TOML|config\.toml/i,
    'the page must not tell the user to restart or hand-edit TOML'
  );
});

test('the alert delivery section renders the recipient list and enable checkbox', () => {
  assert.match(settings, /bind:value=\{smtpToText\}/, 'recipients must bind to the comma-separated field');
  assert.match(settings, /bind:checked=\{smtpEnabled\}/, 'SMTP enabled must be a checkbox bound to smtpEnabled');
});
