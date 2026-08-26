/**
 * Proves the skeleton pulse is really suppressed for a reduced-motion
 * preference, rather than trusting the class name.
 *
 * The rendering tests assert the markup asks for `motion-safe:animate-pulse`.
 * This test runs the project's own Tailwind pipeline over the skeleton
 * components and checks the emitted CSS puts the animation behind
 * `prefers-reduced-motion: no-preference` and nowhere else.
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';

import postcss from 'postcss';
import tailwindcss from 'tailwindcss';

import tailwindConfig from '../tailwind.config.js';

const skeletonSources = fileURLToPath(
  new URL('../src/lib/components/Skeleton*.svelte', import.meta.url)
);

async function buildUtilities() {
  const result = await postcss([
    tailwindcss({ ...tailwindConfig, content: [skeletonSources] })
  ]).process('@tailwind utilities;', { from: undefined });
  return result.css;
}

test('the skeleton pulse only runs when motion is allowed', async () => {
  const css = await buildUtilities();

  assert.match(
    css,
    /animation:\s*pulse/,
    'the skeleton components must generate the pulse animation utility'
  );

  const guardedBlocks = css.match(
    /@media\s*\(prefers-reduced-motion:\s*no-preference\)\s*\{[\s\S]*?animation:\s*pulse[\s\S]*?\}/g
  );
  assert.ok(
    guardedBlocks && guardedBlocks.length > 0,
    'the pulse must be emitted inside a prefers-reduced-motion: no-preference query'
  );

  // Strip the guarded queries; anything left that still animates would keep
  // pulsing for a user who asked for reduced motion.
  const unguarded = css.replace(
    /@media\s*\(prefers-reduced-motion:\s*no-preference\)\s*\{[\s\S]*?\n\}/g,
    ''
  );
  assert.doesNotMatch(
    unguarded,
    /animation:\s*pulse/,
    'no pulse animation may be emitted outside the reduced-motion guard'
  );
});
