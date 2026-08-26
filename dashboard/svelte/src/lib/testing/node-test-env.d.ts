/**
 * Minimal ambient declarations for the Node built-ins used by the `*.test.ts`
 * unit tests, which run under `node --test`.
 *
 * Declaring just this surface keeps the tests type-checked by `svelte-check` and
 * `tsc --noEmit` without pulling in the whole `@types/node` package, which the
 * dashboard does not otherwise need.
 */

declare module 'node:test' {
  export function test(name: string, fn: () => void | Promise<void>): void;
}

declare module 'node:assert/strict' {
  interface StrictAssert {
    (value: unknown, message?: string): asserts value;
    ok(value: unknown, message?: string): asserts value;
    equal<T>(actual: unknown, expected: T, message?: string): void;
    notEqual(actual: unknown, expected: unknown, message?: string): void;
    deepEqual<T>(actual: unknown, expected: T, message?: string): void;
    notDeepEqual(actual: unknown, expected: unknown, message?: string): void;
    match(value: string, pattern: RegExp, message?: string): void;
    doesNotMatch(value: string, pattern: RegExp, message?: string): void;
    fail(message?: string): never;
  }

  const assert: StrictAssert;
  export default assert;
}

declare const process: {
  env: Record<string, string | undefined>;
};
