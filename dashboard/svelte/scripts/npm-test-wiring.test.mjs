/**
 * Guards against the component-state checks silently falling out of CI.
 * `npm test` is the command CI actually runs, so this resolves that one script
 * (following any `npm run X` it chains to) and checks each suite is really
 * reachable from it, not just present on disk.
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';

const pkg = JSON.parse(readFileSync(new URL('../package.json', import.meta.url), 'utf8'));

function resolveScript(name, seen = new Set()) {
  if (seen.has(name)) {
    return '';
  }
  seen.add(name);

  const raw = pkg.scripts[name];
  if (!raw) {
    return '';
  }

  return raw.replace(/npm run ([\w:-]+)/g, (_match, sub) => resolveScript(sub, seen));
}

test('npm test runs every script-based suite in scripts/', () => {
  const resolved = resolveScript('test');
  assert.ok(resolved.length > 0, 'package.json must define a "test" script');

  for (const file of [
    'scripts/skeleton.render.test.mjs',
    'scripts/dashboard-states.contract.test.mjs',
    'scripts/reduced-motion.css.test.mjs',
    'scripts/provider-status.contract.test.mjs',
    'scripts/agent-delete.render.test.mjs',
    'scripts/check-agents-delete.mjs',
    'scripts/upgrade.contract.test.mjs'
  ]) {
    assert.match(
      resolved,
      new RegExp(file.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')),
      `npm test must run ${file} so CI protects it`
    );
  }
});

// The list above has to be extended by hand every time a suite is added, which
// is exactly what was missed once already. This catches the omission instead.
test('no suite in scripts/ is left out of npm test', () => {
  const resolved = resolveScript('test');
  const suites = readdirSync(new URL('.', import.meta.url))
    .filter((entry) => entry.endsWith('.test.mjs') || entry.startsWith('check-'))
    .filter((entry) => entry !== 'npm-test-wiring.test.mjs');

  const missing = suites.filter((entry) => !resolved.includes(`scripts/${entry}`));
  assert.deepEqual(missing, [], `these suites are on disk but not reachable from npm test: ${missing.join(', ')}`);
});
