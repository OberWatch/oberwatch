/**
 * Contract checks for the loading, empty, error and retry states of the shipped
 * dashboard pages.
 *
 * The components themselves are covered by rendering tests. What these checks
 * defend is the wiring: that every page routes its states through the one
 * tested `loadPhase` helper and the shared variants, that none of them keeps a
 * private copy of the pulse markup or the error banner, and that retrying does
 * not throw away the filter or form the user was working with.
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

function read(relativePath) {
  return readFileSync(new URL(relativePath, import.meta.url), 'utf8');
}

const pages = {
  Overview: read('../src/routes/+page.svelte'),
  Agents: read('../src/routes/agents/+page.svelte'),
  Costs: read('../src/routes/costs/+page.svelte'),
  Settings: read('../src/routes/settings/+page.svelte')
};

const skeletonVariant = /Skeleton(KPICard|Table|Chart)/;

test('every covered page drives its states through the tested loadPhase helper', () => {
  for (const [name, source] of Object.entries(pages)) {
    assert.match(
      source,
      /import\s*\{[^}]*loadPhase[^}]*\}\s*from\s*'\$lib\/loadState'/,
      `${name} must decide loading/error/empty/ready with the tested helper`
    );
    assert.match(
      source,
      /loadPhase\(/,
      `${name} must actually call loadPhase`
    );
    assert.doesNotMatch(
      source,
      /\{#if\s+loading\s*\}/,
      `${name} must not branch on a raw loading flag that bypasses loadPhase`
    );
  }
});

test('every covered page uses the shared skeleton variants', () => {
  for (const [name, source] of Object.entries(pages)) {
    assert.match(
      source,
      skeletonVariant,
      `${name} must render a shared skeleton variant while loading`
    );
    assert.match(
      source,
      /import\s*\{[^}]*Skeleton[^}]*\}\s*from\s*'\$lib\/components'/,
      `${name} must import its skeletons from the shared component barrel`
    );
  }
});

test('no page keeps a private copy of the pulse markup', () => {
  for (const [name, source] of Object.entries(pages)) {
    assert.doesNotMatch(
      source,
      /animate-pulse/,
      `${name} must not hand-roll pulse markup; use the shared Skeleton variants`
    );
  }
});

test('no page keeps a private copy of the error banner', () => {
  for (const [name, source] of Object.entries(pages)) {
    assert.match(
      source,
      /import\s*\{[^}]*ErrorState[^}]*\}\s*from\s*'\$lib\/components'/,
      `${name} must render API failures through the shared ErrorState`
    );
    assert.match(source, /<ErrorState\b/, `${name} must render ErrorState`);
    assert.doesNotMatch(
      source,
      /border-danger\/40/,
      `${name} must not duplicate the error banner styling`
    );
  }
});

test('no page shows a textual loading placeholder any more', () => {
  for (const [name, source] of Object.entries(pages)) {
    assert.doesNotMatch(
      source,
      /Loading[^'"}<]*\.\.\./,
      `${name} must show a skeleton rather than a "Loading..." string`
    );
  }
});

test('loaded-empty states are only reachable from the empty phase', () => {
  const emptyStates = [
    { name: 'Overview', source: pages.Overview, marker: /No cost data yet/ },
    { name: 'Agents', source: pages.Agents, marker: /No agents detected yet/ },
    { name: 'Costs', source: pages.Costs, marker: /No cost data available/ },
    { name: 'Settings', source: pages.Settings, marker: /No configured model pricing/ }
  ];

  for (const { name, source, marker } of emptyStates) {
    assert.match(source, marker, `${name} must still explain a genuinely empty result`);
  }

  // A zero KPI is the clearest case of a loaded-empty value: it looks like a
  // real reading. It must be replaced by a placeholder until the load lands.
  assert.match(
    pages.Overview,
    /\{#if\s+\w*[Pp]hase\s*===\s*'loading'\}[\s\S]{0,600}?<SkeletonKPICard/,
    'Overview must show KPI placeholders instead of zeroes while loading'
  );
  assert.match(
    pages.Costs,
    /\{#if\s+\w*[Pp]hase\s*===\s*'loading'\}[\s\S]{0,400}?<SkeletonKPICard/,
    'Costs must show a Total Cost placeholder instead of $0.00 while loading'
  );

  for (const [name, source] of Object.entries(pages)) {
    assert.doesNotMatch(
      source,
      /\{:else if\s+\w+\.length\s*===\s*0\s*\}/,
      `${name} must not fall into an empty state directly from a length check`
    );
  }
});

test('Overview retry reloads without discarding the emergency-stop banner', () => {
  assert.match(
    pages.Overview,
    /<ErrorState[\s\S]{0,200}?onRetry=\{(\(\)\s*=>\s*)?loadOverview(\(\))?\}/,
    'Overview retry must re-run the overview load'
  );
  // The banner and the KPI row sit outside the error branch, so a failed load
  // leaves the emergency-stop control reachable.
  assert.match(
    pages.Overview,
    /\{#if emergencyStopActive\}/,
    'the emergency-stop banner must stay independent of the load state'
  );
});

test('Agents retry preserves the active name filter', () => {
  assert.match(
    pages.Agents,
    /<ErrorState[\s\S]{0,200}?onRetry=\{(\(\)\s*=>\s*)?loadAgents(\(\))?\}/,
    'Agents retry must re-run the agents load'
  );
  assert.doesNotMatch(
    pages.Agents,
    /\bsearch\s*=\s*''/,
    'reloading agents must never clear the name filter the user typed'
  );
  assert.match(
    pages.Agents,
    /filteredRows/,
    'the filter must still be applied to the reloaded rows'
  );
});

test('Costs retry preserves the active range selection', () => {
  assert.match(
    pages.Costs,
    /<ErrorState[\s\S]{0,200}?onRetry=\{(\(\)\s*=>\s*)?retry(\(\))?\}/,
    'Costs retry must go through the retry function'
  );
  assert.match(
    pages.Costs,
    /function\s+retry\s*\([^)]*\)[\s\S]{0,200}?loadCosts\(\s*selection\s*\)/,
    'Costs retry must reload the range the user currently has selected'
  );
  assert.doesNotMatch(
    pages.Costs,
    /\bselection\s*=\s*\{\s*preset:\s*'today'[\s\S]{0,80}?\}\s*;?\s*$/m,
    'retrying must not reset the range picker back to its default'
  );
});

test('Settings retry preserves the password form the user was filling in', () => {
  assert.match(
    pages.Settings,
    /<ErrorState[\s\S]{0,200}?onRetry=\{(\(\)\s*=>\s*)?loadSettings(\(\))?\}/,
    'Settings retry must re-run the settings load'
  );

  const loader = pages.Settings.match(/async function loadSettings[\s\S]*?\n  \}/);
  assert.ok(loader, 'Settings must still have a loadSettings function');
  for (const field of ['currentPassword', 'newPassword', 'confirmPassword']) {
    assert.doesNotMatch(
      loader[0],
      new RegExp(`\\b${field}\\s*=`),
      `reloading settings must not clear ${field}`
    );
  }
});

test('Settings pricing failure is a nonfatal accessible status with its own retry, and never clears the password form', () => {
  // Pricing is one section of a page that otherwise loaded fine, so it must not
  // be reported through the fatal, page-replacing ErrorState/role="alert" — a
  // degraded status region is what lets the rest of the page, including the
  // in-progress password form, stay on screen and usable.
  assert.match(
    pages.Settings,
    /\{#if\s+pricingWarning\}[\s\S]{0,200}?role="status"/,
    'a pricing failure must render inside an accessible, nonfatal status region'
  );

  assert.match(
    pages.Settings,
    /\{#if\s+pricingWarning\}[\s\S]{0,600}?onclick=\{(\(\)\s*=>\s*)?loadPricing(\(\))?\}/,
    'the pricing status must offer its own targeted retry'
  );

  const pricingLoader = pages.Settings.match(/async function loadPricing\s*\([^)]*\)[\s\S]*?\n  \}/);
  assert.ok(pricingLoader, 'Settings must define a dedicated pricing retry function, separate from loadSettings');

  assert.doesNotMatch(
    pricingLoader[0],
    /\bloading\s*=\s*true/,
    'retrying pricing alone must not flip the page-wide loading flag and hide the password form'
  );

  for (const field of ['currentPassword', 'newPassword', 'confirmPassword']) {
    assert.doesNotMatch(
      pricingLoader[0],
      new RegExp(`\\b${field}\\s*=`),
      `retrying pricing must not clear ${field}`
    );
  }
});

test('Costs clears a stale API error when the custom range fails validation', () => {
  // rangeError (the validation message next to the date inputs) and errorMessage
  // (the page-wide ErrorState) are reported through two different surfaces. If a
  // previous API failure left errorMessage set, picking an invalid custom range
  // must not leave both visible at once.
  const invalidBranch = pages.Costs.match(/if\s*\(outcome\.status === 'invalid'\)\s*\{[\s\S]*?\n\s*\}/);
  assert.ok(invalidBranch, 'Costs must still handle the invalid-range outcome');
  assert.match(
    invalidBranch[0],
    /errorMessage\s*=\s*null/,
    'an invalid custom range must clear a prior API error so ErrorState does not coexist with the validation message'
  );
});
