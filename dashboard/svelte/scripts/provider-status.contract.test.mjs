/**
 * Contract checks for the Settings provider cards (Issue #85).
 *
 * The status helpers are unit tested in src/lib/providerStatus.test.ts. What
 * these checks defend is the page wiring: that the cards are refreshed while
 * the page stays open, that a refresh never hides the page behind the skeleton
 * or blanks the grid, and that the card set is whatever the API returned rather
 * than something the page filters or gates on its own.
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

function read(relativePath) {
  return readFileSync(new URL(relativePath, import.meta.url), 'utf8');
}

const settings = read('../src/routes/settings/+page.svelte');
const types = read('../src/lib/types.ts');

function functionBody(source, name) {
  const match = source.match(new RegExp(`(?:async )?function ${name}\\s*\\([^)]*\\)[^{]*\\{[\\s\\S]*?\\n  \\}`));
  assert.ok(match, `Settings must define ${name}`);
  return match[0];
}

test('the provider status types carry the unreachable state and the probe time', () => {
  const availability = types.match(/export type ProviderAvailability\s*=([\s\S]*?);/);
  assert.ok(availability, 'ProviderAvailability must be exported');
  for (const value of ['operational', 'degraded', 'outage', 'status_unavailable', 'unreachable']) {
    assert.match(availability[1], new RegExp(`'${value}'`), `ProviderAvailability must include ${value}`);
  }

  const providerStatus = types.match(/export interface ProviderStatus\s*\{([\s\S]*?)\n\}/);
  assert.ok(providerStatus, 'ProviderStatus must be exported');
  assert.match(providerStatus[1], /observed_at\?: string/, 'ProviderStatus must carry the optional observed_at the API sends');
});

test('Settings renders provider cards through the tested helpers', () => {
  assert.match(
    settings,
    /import\s*\{[^}]*\bnextProviderRows\b[^}]*\}\s*from\s*'\$lib\/providerStatus'/,
    'Settings must decide the next card set with nextProviderRows'
  );
  assert.match(
    settings,
    /import\s*\{[^}]*\bproviderStatusLabel\b[^}]*\}\s*from\s*'\$lib\/providerStatus'/,
    'Settings must label cards with the shared helper'
  );
  assert.doesNotMatch(
    settings,
    /function providerStatusLabel|function providerBadgeStatus/,
    'Settings must not keep a private copy of the status label or badge mapping'
  );
  assert.doesNotMatch(
    settings,
    /detail\.startsWith\(/,
    'the pending state must come from observed_at, not from matching detail text'
  );
  assert.match(
    settings,
    /providerBadgeStatus\(provider\)/,
    'the badge must be chosen from the whole row, so a never-probed row is not coloured as a failure'
  );
});

test('Settings refreshes provider status on an interval while the page is open and stops when it leaves', () => {
  assert.match(
    settings,
    /import\s*\{[^}]*\bPROVIDER_REFRESH_INTERVAL_MS\b[^}]*\}\s*from\s*'\$lib\/providerStatus'/,
    'the refresh cadence must come from the tested constant'
  );
  assert.match(
    settings,
    /setInterval\([\s\S]{0,300}?refreshProviderStatus\(\)[\s\S]{0,200}?PROVIDER_REFRESH_INTERVAL_MS\)/,
    'Settings must schedule refreshProviderStatus on PROVIDER_REFRESH_INTERVAL_MS'
  );
  assert.match(
    settings,
    /onMount\(\(\)\s*=>\s*\{[\s\S]*?const (\w+)\s*=\s*setInterval[\s\S]*?return\s*\(\)\s*=>\s*\{[\s\S]*?clearInterval\(\1\)/,
    'the interval must be cleared when the page unmounts'
  );
});

test('a provider refresh never hides the page or blanks the grid', () => {
  const refresh = functionBody(settings, 'refreshProviderStatus');

  assert.doesNotMatch(
    refresh,
    /\bloading\s*=\s*true/,
    'refreshing provider status must not flip the page-wide loading flag and unmount the password form'
  );
  assert.doesNotMatch(
    refresh,
    /\bproviderRows\s*=\s*\[\]/,
    'refreshing must not clear the cards before the new rows arrive'
  );
  assert.doesNotMatch(
    refresh,
    /\berrorMessage\s*=/,
    'a failed refresh is a nonfatal condition; it must not replace the page with ErrorState'
  );
  assert.match(
    refresh,
    /nextProviderRows\(\s*providerRows\s*,\s*\{\s*ok:\s*false\s*\}\s*\)/,
    'a failed refresh must keep the previous cards through nextProviderRows'
  );

  for (const field of ['currentPassword', 'newPassword', 'confirmPassword']) {
    assert.doesNotMatch(refresh, new RegExp(`\\b${field}\\s*=`), `refreshing must not clear ${field}`);
  }

  const loader = functionBody(settings, 'loadSettings');
  assert.doesNotMatch(
    loader,
    /\bproviderRows\s*=\s*\[\]/,
    'retrying the settings load must keep the last known cards instead of blanking the grid'
  );
});

test('a refresh is bounded: one read at a time, and none while a full load owns the page', () => {
  const refresh = functionBody(settings, 'refreshProviderStatus');

  const guard = refresh.match(/if \(([^)]*)\) return;/);
  assert.ok(guard, 'refreshProviderStatus must guard its entry');
  for (const condition of ['loading', 'errorMessage', 'refreshInFlight']) {
    assert.match(
      guard[1],
      new RegExp(`\\b${condition}\\b`),
      `the refresh must stand down on ${condition}`
    );
  }

  assert.match(
    refresh,
    /refreshInFlight = true;[\s\S]*?finally \{\s*refreshInFlight = false;\s*\}/,
    'the in-flight flag must be released on every path, or one failure stops the refresh for good'
  );
});

test('a failed refresh is reported as a nonfatal status next to the cards', () => {
  assert.match(
    settings,
    /\{#if\s+providerRefreshWarning\}[\s\S]{0,200}?role="status"/,
    'a refresh failure must render inside an accessible, nonfatal status region'
  );
});

test('the card set is exactly what the API returned', () => {
  const section = settings.match(/Provider Status<\/h2>[\s\S]*?Model Pricing<\/h2>/);
  assert.ok(section, 'Settings must still have the Provider Status section');

  assert.match(section[0], /\{#each providerRows as provider \(provider\.provider\)\}/, 'cards must be keyed by provider so a status change updates in place');
  assert.doesNotMatch(
    section[0],
    /providerRows\.(filter|length)/,
    'the page must not filter or gate the cards; the API decides which providers exist'
  );
  assert.doesNotMatch(
    section[0],
    /\{#if\s+provider\.status/,
    'a card must render whatever its status is'
  );
  assert.doesNotMatch(
    section[0],
    /md:grid-cols-\d/,
    'a fixed column count leaves an empty cell when a card is missing; the grid must fit the cards it has'
  );
  assert.match(section[0], /auto-fit/, 'the provider grid must size itself to the number of cards');
});
