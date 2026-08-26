/**
 * The four states every async dashboard section can be in.
 *
 * `empty` means the request succeeded and there was nothing in it. Keeping it
 * distinct from `loading` and from `error` stops a page from presenting a zero
 * or an empty list as a fetched result when nothing was fetched.
 */
export type LoadPhase = 'loading' | 'error' | 'empty' | 'ready';

export interface LoadStateInput {
  /** True while a request for this section is in flight. */
  loading: boolean;
  /** The message from the last failed request, or null after a success. */
  errorMessage: string | null;
  /** True when the last successful load produced something to show. */
  hasData: boolean;
}

/**
 * loadPhase reduces a section's flags to the single state its markup renders.
 *
 * A request in flight wins over everything, so a retry replaces a stale error
 * with placeholders instead of showing both. A failure then wins over the empty
 * state, so a failed load is never mistaken for "no data".
 */
export function loadPhase({ loading, errorMessage, hasData }: LoadStateInput): LoadPhase {
  if (loading) {
    return 'loading';
  }
  if (errorMessage) {
    return 'error';
  }
  return hasData ? 'ready' : 'empty';
}
