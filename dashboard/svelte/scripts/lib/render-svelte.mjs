/**
 * Minimal server-side render harness for the dashboard components.
 *
 * The dashboard has no browser test runner. Compiling a component with the
 * Svelte compiler's server target and rendering it with `svelte/server` gives
 * real markup to assert against, which is what the loading/error/retry states
 * need: their whole job is the DOM they produce.
 *
 * Compiled output is written under `.svelte-kit/` so it stays out of git and so
 * bare imports such as `svelte/internal/server` still resolve through the
 * dashboard's own `node_modules`.
 */
import { compile } from 'svelte/compiler';
import { render as renderServer } from 'svelte/server';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { basename, dirname, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const projectRoot = fileURLToPath(new URL('../../', import.meta.url));
const srcRoot = resolve(projectRoot, 'src');
const outRoot = resolve(projectRoot, '.svelte-kit/render-tests');

/** Compiled modules are cached so a shared child is only built once. */
const compiledByPath = new Map();

function outputPathFor(componentPath) {
  const flattened = relative(srcRoot, componentPath).replace(/[\\/]/g, '__');
  return resolve(outRoot, `${flattened}.js`);
}

function resolveLocalSpecifier(specifier, importerDir) {
  if (specifier.startsWith('$lib/')) {
    return resolve(srcRoot, 'lib', specifier.slice('$lib/'.length));
  }
  if (specifier.startsWith('.')) {
    return resolve(importerDir, specifier);
  }
  return null;
}

function compileComponent(componentPath) {
  const cached = compiledByPath.get(componentPath);
  if (cached) {
    return cached;
  }

  const outputPath = outputPathFor(componentPath);
  // Registered before compiling children so an import cycle terminates.
  compiledByPath.set(componentPath, outputPath);

  const source = readFileSync(componentPath, 'utf8');
  const { js } = compile(source, { generate: 'server', filename: componentPath });

  const code = js.code.replace(/from\s*'([^']+)'/g, (match, specifier) => {
    const target = resolveLocalSpecifier(specifier, dirname(componentPath));
    if (target === null) {
      return match;
    }
    if (!target.endsWith('.svelte')) {
      throw new Error(
        `render-svelte can only follow .svelte imports, got '${specifier}' from ${componentPath}`
      );
    }
    return `from './${basename(compileComponent(target))}'`;
  });

  mkdirSync(outRoot, { recursive: true });
  writeFileSync(outputPath, code);
  return outputPath;
}

/**
 * render compiles `srcRelativePath` (relative to `src/`) and returns its
 * server-rendered body markup.
 *
 * @param {string} srcRelativePath
 * @param {Record<string, unknown>} [props]
 * @returns {Promise<string>}
 */
export async function render(srcRelativePath, props = {}) {
  const outputPath = compileComponent(resolve(srcRoot, srcRelativePath));
  const module = await import(outputPath);
  return renderServer(module.default, { props }).body;
}

/**
 * countOccurrences counts non-overlapping matches of `needle` in `haystack`.
 *
 * @param {string} haystack
 * @param {string} needle
 * @returns {number}
 */
export function countOccurrences(haystack, needle) {
  return haystack.split(needle).length - 1;
}
