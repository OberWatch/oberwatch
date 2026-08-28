/**
 * Guards against the contract suites silently falling out of CI. `npm test` is
 * the command CI actually runs, so this resolves that one script (following any
 * `npm run X` it chains to) and checks each suite is really reachable from it,
 * not just present on disk.
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

test('npm test runs every dashboard contract suite', () => {
  const resolved = resolveScript('test');
  assert.ok(resolved.length > 0, 'package.json must define a "test" script');

  for (const file of [
    'scripts/skeleton.render.test.mjs',
    'scripts/dashboard-states.contract.test.mjs',
    'scripts/reduced-motion.css.test.mjs',
    'scripts/provider-status.contract.test.mjs',
    'scripts/upgrade.contract.test.mjs'
  ]) {
    assert.match(
      resolved,
      new RegExp(file.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')),
      `npm test must run ${file} so CI protects it`
    );
  }
});
