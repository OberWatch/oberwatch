import type { AlertSettingsPatchRequest, AlertSettingsResponse } from './types';

/**
 * Editable state for the Alert delivery form. The three secret fields
 * (`smtpPasswordInput`, `webhookInput`, `slackInput`) never carry a value read
 * from the API — the API never sends secrets back, only `*_is_set` booleans —
 * so they always start blank and only hold what the user just typed.
 */
export interface AlertSettingsFormState {
  smtpEnabled: boolean;
  smtpHost: string;
  smtpPort: string;
  smtpUser: string;
  smtpFrom: string;
  smtpToText: string;
  smtpPasswordInput: string;
  webhookInput: string;
  slackInput: string;
}

export type SecretField = 'smtp_password' | 'webhook_url' | 'slack_webhook_url';

/** alertSettingsToFormState seeds the form from a load or reload. Secrets stay blank. */
export function alertSettingsToFormState(settings: AlertSettingsResponse): AlertSettingsFormState {
  return {
    smtpEnabled: settings.smtp_enabled,
    smtpHost: settings.smtp_host,
    smtpPort: settings.smtp_port ? String(settings.smtp_port) : '',
    smtpUser: settings.smtp_user,
    smtpFrom: settings.smtp_from,
    smtpToText: formatRecipients(settings.smtp_to),
    smtpPasswordInput: '',
    webhookInput: '',
    slackInput: ''
  };
}

/** parseRecipients turns the comma-separated field into a clean address list. */
export function parseRecipients(text: string): string[] {
  return text
    .split(',')
    .map((entry) => entry.trim())
    .filter((entry) => entry.length > 0);
}

/** formatRecipients is the inverse of parseRecipients, for seeding the field. */
export function formatRecipients(recipients: string[]): string {
  return recipients.join(', ');
}

function sameRecipients(a: string[], b: string[]): boolean {
  return a.length === b.length && a.every((value, index) => value === b[index]);
}

/** secretStatusLabel renders an `*_is_set` flag without ever showing the secret itself. */
export function secretStatusLabel(isSet: boolean): string {
  return isSet ? 'Configured' : 'Not configured';
}

/**
 * buildAlertSettingsPatch diffs the form against the last loaded settings so a
 * save only sends what changed. A secret field left blank is treated as
 * "unchanged" and omitted entirely — an empty PATCH value for a secret means
 * "clear it", so a blank box must never be sent as a save side effect. Only
 * the dedicated clear controls (see `clearSecretPatch`) send that value.
 */
export function buildAlertSettingsPatch(
  original: AlertSettingsResponse,
  form: AlertSettingsFormState
): AlertSettingsPatchRequest {
  const patch: AlertSettingsPatchRequest = {};

  if (form.smtpEnabled !== original.smtp_enabled) {
    patch.smtp_enabled = form.smtpEnabled;
  }
  if (form.smtpHost !== original.smtp_host) {
    patch.smtp_host = form.smtpHost;
  }
  const port = Number.parseInt(form.smtpPort, 10);
  if (Number.isFinite(port) && port !== original.smtp_port) {
    patch.smtp_port = port;
  }
  if (form.smtpUser !== original.smtp_user) {
    patch.smtp_user = form.smtpUser;
  }
  if (form.smtpFrom !== original.smtp_from) {
    patch.smtp_from = form.smtpFrom;
  }

  const recipients = parseRecipients(form.smtpToText);
  if (!sameRecipients(recipients, original.smtp_to)) {
    patch.smtp_to = recipients;
  }

  if (form.smtpPasswordInput.trim().length > 0) {
    patch.smtp_password = form.smtpPasswordInput;
  }
  if (form.webhookInput.trim().length > 0) {
    patch.webhook_url = form.webhookInput;
  }
  if (form.slackInput.trim().length > 0) {
    patch.slack_webhook_url = form.slackInput;
  }

  return patch;
}

/** hasAlertSettingsChanges tells a save button whether there is anything to send. */
export function hasAlertSettingsChanges(patch: AlertSettingsPatchRequest): boolean {
  return Object.keys(patch).length > 0;
}

/**
 * clearSecretPatch is the only place an empty string is ever sent for a
 * secret. It backs the explicit "Clear" controls, kept separate from
 * buildAlertSettingsPatch so an empty save field can never be mistaken for a
 * clear request.
 */
export function clearSecretPatch(field: SecretField): AlertSettingsPatchRequest {
  return { [field]: '' } as AlertSettingsPatchRequest;
}
