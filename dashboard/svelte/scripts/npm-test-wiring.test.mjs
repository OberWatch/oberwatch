/**
 * Guards against the Issue #19 component-state checks silently falling out of
 * CI. `npm test` is the command CI actually runs, so this resolves that one
 * script (following any `npm run X` it chains to) and checks each of the three
 * new suites is really reachable from it, not just present on disk.
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

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

test('npm test runs the skeleton render, dashboard states and reduced motion suites', () => {
  const resolved = resolveScript('test');
  assert.ok(resolved.length > 0, 'package.json must define a "test" script');

  for (const file of [
    'scripts/skeleton.render.test.mjs',
    'scripts/dashboard-states.contract.test.mjs',
    'scripts/reduced-motion.css.test.mjs'
  ]) {
    assert.match(
      resolved,
      new RegExp(file.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')),
      `npm test must run ${file} so CI protects it`
    );
  }
});
