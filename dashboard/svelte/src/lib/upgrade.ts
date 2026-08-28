import type { UpgradeResult, UpgradeStatusResponse } from './types';

/**
 * How often the sidebar re-reads upgrade status while an upgrade is being
 * applied. Reads only happen during that window: the version check itself is
 * cached on the server and is not polled.
 */
export const UPGRADE_POLL_INTERVAL_MS = 3_000;

/**
 * How long the sidebar waits for the service to come back before it stops
 * waiting and says so. It never claims an outcome it did not observe.
 */
export const UPGRADE_POLL_TIMEOUT_MS = 120_000;

/**
 * What the sidebar footer shows next to the running version.
 *
 * - `checking`: no release check has finished yet.
 * - `current`: the running version is the newest stable release.
 * - `available`: a newer stable release exists and can be installed here.
 * - `unsupported`: a newer release exists but this installation cannot apply it.
 * - `unavailable`: the release check could not be completed.
 */
export type UpgradeViewKind = 'checking' | 'current' | 'available' | 'unsupported' | 'unavailable';

export interface UpgradeView {
  kind: UpgradeViewKind;
  /** The button label, present only for `available`. */
  action: string | null;
  /** The line shown next to the version, or null when there is nothing to add. */
  note: string | null;
}

/** The severity of a recorded outcome, used to pick its styling. */
export type UpgradeTone = 'success' | 'warning' | 'error';

export interface UpgradeNote {
  tone: UpgradeTone;
  text: string;
}

/**
 * upgradeButtonLabel renders the action label for a release tag. The API sends
 * a `v`-prefixed tag; the prefix is normalised here rather than assumed, so the
 * label never reads "Upgrade to 0.1.4" or "Upgrade to vv0.1.4".
 */
export function upgradeButtonLabel(latestVersion: string): string {
  const trimmed = latestVersion.trim();
  const tag = trimmed.startsWith('v') ? trimmed : `v${trimmed}`;
  return `Upgrade to ${tag}`;
}

/**
 * upgradeView decides what the footer shows.
 *
 * The order of the checks is the point. A release that was actually observed to
 * be newer is reported even when a later check failed, because the operator
 * still needs to know it exists. A failed check is never reported as "up to
 * date", and nothing is claimed before the first check finishes.
 */
export function upgradeView(status: UpgradeStatusResponse | null): UpgradeView {
  if (status === null) {
    return { kind: 'checking', action: null, note: 'Checking for updates' };
  }

  const latest = status.latest_version?.trim() ?? '';
  if (status.update_available && latest !== '') {
    if (status.supported) {
      return { kind: 'available', action: upgradeButtonLabel(latest), note: null };
    }
    const fallback = status.fallback?.trim();
    const reason = fallback && fallback !== '' ? fallback : 'Upgrading from the dashboard is not available for this installation.';
    return { kind: 'unsupported', action: null, note: `${latest} is available. ${reason}` };
  }

  const checkError = status.check_error?.trim() ?? '';
  if (checkError !== '') {
    return { kind: 'unavailable', action: null, note: 'Update check unavailable' };
  }
  if (!status.checked_at) {
    return { kind: 'checking', action: null, note: 'Checking for updates' };
  }
  return { kind: 'current', action: null, note: null };
}

/**
 * upgradeConfirmationLines is the disclosure shown before an upgrade starts.
 *
 * Each line states something the operator has to know to consent: what is
 * verified, that the service restarts and is briefly unavailable, that their
 * configuration and data are untouched, and that the previous binary is kept so
 * the upgrade can be undone.
 */
export function upgradeConfirmationLines(latestVersion: string): string[] {
  const trimmed = latestVersion.trim();
  const tag = trimmed.startsWith('v') ? trimmed : `v${trimmed}`;
  return [
    `Oberwatch downloads release ${tag} and verifies it against the checksum published with that release.`,
    'The service then restarts. The dashboard and the proxy are unavailable for a few seconds, and requests in flight are dropped.',
    'Your configuration file and database are not touched.',
    'The version being replaced is kept on disk, so the upgrade can be rolled back.'
  ];
}

/** Short prefixes for the refusals the upgrade endpoint can answer with. */
const UPGRADE_FAILURE_MESSAGES: Record<string, string> = {
  upgrade_unsupported: 'This installation cannot apply an upgrade from the dashboard. Nothing was changed.',
  upgrade_not_available: 'There is no newer stable release to install.',
  upgrade_in_progress: 'An upgrade is already running.',
  upgrade_verification_failed:
    'The downloaded release did not match the checksum published with it, so nothing was installed.',
  upgrade_source_unavailable: 'The release could not be downloaded, so nothing was installed.'
};

/**
 * upgradeFailureMessage turns a refusal into a line an operator can act on. An
 * unrecognised code falls back to whatever the server said, so a failure is
 * never reported as an empty message.
 */
export function upgradeFailureMessage(code: string | undefined, detail: string): string {
  const known = code ? UPGRADE_FAILURE_MESSAGES[code] : undefined;
  if (known) {
    return known;
  }
  const trimmed = detail.trim();
  return trimmed === '' ? 'The upgrade failed.' : trimmed;
}

/**
 * upgradeResultNote renders the outcome the privileged installer recorded, or
 * null when there is none. A `restart_required` outcome is deliberately not
 * reported as a success: the old version is still the one running.
 */
export function upgradeResultNote(result: UpgradeResult | null | undefined): UpgradeNote | null {
  if (!result) {
    return null;
  }
  const message = result.message.trim();
  switch (result.status) {
    case 'succeeded':
      return { tone: 'success', text: `Updated to ${result.tag}.` };
    case 'restart_required':
      return {
        tone: 'warning',
        text: `${result.tag} is installed, but the service was not restarted, so the previous version is still running. ${message}`.trim()
      };
    case 'failed':
      return {
        tone: 'error',
        text: `Upgrading to ${result.tag} failed. ${message}`.trim()
      };
    default:
      return null;
  }
}

/** What to do next while waiting for the service to come back. */
export type UpgradePollDecision = 'continue' | 'done' | 'timeout';

export interface UpgradePollInput {
  /** The status that was just read, or null when the read failed. */
  status: UpgradeStatusResponse | null;
  /** The release the upgrade is moving to. */
  target: string;
  /** How long the wait has been going on. */
  elapsedMs: number;
}

/**
 * upgradePollDecision decides whether to keep waiting for the restart.
 *
 * A failed read is not an outcome: during the restart the server is down, and
 * treating that as a failure would report one that did not happen. Waiting stops
 * when the installer recorded an outcome for this release, when the running
 * version is the target, or when the wait has gone on too long to keep guessing.
 */
export function upgradePollDecision(input: UpgradePollInput): UpgradePollDecision {
  const { status, target, elapsedMs } = input;
  if (status !== null) {
    if (status.last_result && status.last_result.tag === target) {
      return 'done';
    }
    if (status.current_version === target) {
      return 'done';
    }
  }
  if (elapsedMs >= UPGRADE_POLL_TIMEOUT_MS) {
    return 'timeout';
  }
  return 'continue';
}
