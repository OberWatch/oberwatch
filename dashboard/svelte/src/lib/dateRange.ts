/**
 * Cost date-range resolution.
 *
 * Every window the Costs page can select is turned into exactly one pair of
 * instants, so the totals, the charts, the table and the CSV export all describe
 * the same slice of time. Boundaries are defined on the viewer's local calendar
 * and then sent to the API as UTC instants:
 *
 * - `today`  — from the start of the current local calendar day, to the current
 *   instant. Not "the last 24 hours": at 23:00 local the window starts 23 hours
 *   ago, and at 00:30 local it starts 30 minutes ago.
 * - `7d`     — from the start of the local calendar day six days before today,
 *   to the current instant. That is seven calendar days including today.
 * - `30d`    — from the start of the local calendar day twenty-nine days before
 *   today, to the current instant. That is thirty calendar days including today.
 * - `custom` — from the start of the local calendar day named by the start
 *   input, to the last millisecond of the local calendar day named by the end
 *   input. Both days are inclusive.
 *
 * Day offsets are applied with calendar arithmetic rather than by subtracting
 * multiples of 24 hours, so a window that spans a daylight-saving transition
 * still begins at a real local midnight.
 */

/** DateRangePreset is the set of selectable cost windows. */
export type DateRangePreset = 'today' | '7d' | '30d' | 'custom';

/** BucketGranularity is the time-series aggregation step for a resolved range. */
export type BucketGranularity = 'hour' | 'day';

/**
 * SERIES_GROUP_BY is the API grouping used for every time series.
 *
 * The series is always fetched as absolute hourly instants and folded into days
 * on the client when the range calls for it. Only the client knows the viewer's
 * calendar: a day truncated from a UTC timestamp splits a local day in two in any
 * negative UTC offset, and it cannot represent the 23- and 25-hour days around a
 * daylight-saving transition.
 */
const SERIES_GROUP_BY = 'agent_hour';

/**
 * DateRangeSelection is the raw picker state. The custom fields hold `yyyy-mm-dd`
 * values as produced by a native date input and are ignored unless the preset is
 * `custom`.
 */
export interface DateRangeSelection {
  preset: DateRangePreset;
  customStart: string;
  customEnd: string;
}

/** ResolvedRange is a validated window expressed as exact UTC instants. */
export interface ResolvedRange {
  preset: DateRangePreset;
  fromISO: string;
  toISO: string;
  bucket: BucketGranularity;
  label: string;
}

/** RangeResolution is either a validated window or the reason it was rejected. */
export type RangeResolution = { ok: true; range: ResolvedRange } | { ok: false; error: string };

/** CALENDAR_DAYS_BY_PRESET is the inclusive calendar-day span of each fixed preset. */
const CALENDAR_DAYS_BY_PRESET: Record<'today' | '7d' | '30d', number> = {
  today: 1,
  '7d': 7,
  '30d': 30
};

const PRESET_LABELS: Record<'today' | '7d' | '30d', string> = {
  today: 'Today',
  '7d': 'Last 7 days',
  '30d': 'Last 30 days'
};

const DATE_INPUT_PATTERN = /^(\d{4})-(\d{2})-(\d{2})$/;
const MILLISECONDS_PER_DAY = 24 * 60 * 60 * 1000;

/**
 * HOURLY_BUCKET_MAX_DAYS is the widest custom window that still reads well at
 * hourly resolution. Anything longer aggregates by day.
 */
const HOURLY_BUCKET_MAX_DAYS = 2;

/**
 * startOfLocalDay returns local midnight of the day `daysBack` calendar days
 * before the day containing `reference`.
 */
function startOfLocalDay(reference: Date, daysBack = 0): Date {
  return new Date(
    reference.getFullYear(),
    reference.getMonth(),
    reference.getDate() - daysBack,
    0,
    0,
    0,
    0
  );
}

/** endOfLocalDay returns the last millisecond of the local day containing `reference`. */
function endOfLocalDay(reference: Date): Date {
  const nextDayStart = new Date(
    reference.getFullYear(),
    reference.getMonth(),
    reference.getDate() + 1,
    0,
    0,
    0,
    0
  );
  return new Date(nextDayStart.getTime() - 1);
}

/**
 * parseLocalDateInput reads a `yyyy-mm-dd` date-input value as local midnight.
 * It returns null for anything that is not a real calendar date, including
 * values the Date constructor would silently roll over such as `2026-13-45`.
 */
function parseLocalDateInput(raw: string): Date | null {
  const match = DATE_INPUT_PATTERN.exec(raw.trim());
  if (!match) {
    return null;
  }

  const year = Number(match[1]);
  const month = Number(match[2]);
  const day = Number(match[3]);
  const candidate = new Date(year, month - 1, day, 0, 0, 0, 0);
  const rolledOver =
    candidate.getFullYear() !== year ||
    candidate.getMonth() !== month - 1 ||
    candidate.getDate() !== day;

  return rolledOver ? null : candidate;
}

/** inclusiveCalendarDays counts the calendar days covered by two local day starts. */
function inclusiveCalendarDays(startDay: Date, endDay: Date): number {
  // Rounding absorbs the extra or missing hour introduced by a DST transition.
  return Math.round((endDay.getTime() - startDay.getTime()) / MILLISECONDS_PER_DAY) + 1;
}

function bucketForSpan(calendarDays: number): BucketGranularity {
  return calendarDays <= HOURLY_BUCKET_MAX_DAYS ? 'hour' : 'day';
}

function resolveCustomRange(selection: DateRangeSelection): RangeResolution {
  const rawStart = selection.customStart.trim();
  const rawEnd = selection.customEnd.trim();

  if (rawStart === '') {
    return { ok: false, error: 'Select a start date.' };
  }
  if (rawEnd === '') {
    return { ok: false, error: 'Select an end date.' };
  }

  const startDay = parseLocalDateInput(rawStart);
  if (!startDay) {
    return { ok: false, error: 'Enter a valid start date.' };
  }
  const endDay = parseLocalDateInput(rawEnd);
  if (!endDay) {
    return { ok: false, error: 'Enter a valid end date.' };
  }
  if (startDay.getTime() > endDay.getTime()) {
    return { ok: false, error: 'Start date must be on or before the end date.' };
  }

  return {
    ok: true,
    range: {
      preset: 'custom',
      fromISO: startDay.toISOString(),
      toISO: endOfLocalDay(endDay).toISOString(),
      bucket: bucketForSpan(inclusiveCalendarDays(startDay, endDay)),
      label: `${rawStart} to ${rawEnd}`
    }
  };
}

/**
 * resolveRange validates a picker selection against `now` and returns the exact
 * instants to query, or the message to show beside the custom inputs.
 */
export function resolveRange(selection: DateRangeSelection, now: Date): RangeResolution {
  if (selection.preset === 'custom') {
    return resolveCustomRange(selection);
  }

  const calendarDays = CALENDAR_DAYS_BY_PRESET[selection.preset];
  const from = startOfLocalDay(now, calendarDays - 1);

  return {
    ok: true,
    range: {
      preset: selection.preset,
      fromISO: from.toISOString(),
      toISO: now.toISOString(),
      bucket: bucketForSpan(calendarDays),
      label: PRESET_LABELS[selection.preset]
    }
  };
}

function instantParams(range: ResolvedRange): string {
  return `from=${encodeURIComponent(range.fromISO)}&to=${encodeURIComponent(range.toISO)}`;
}

/**
 * costsQuery builds the raw-row query behind the totals, the per-agent and
 * per-model bars and the table.
 */
export function costsQuery(range: ResolvedRange): string {
  return `group_by=none&${instantParams(range)}`;
}

/**
 * costsExportQuery builds the CSV query. It delegates to costsQuery so the
 * export can never drift from what the page is showing.
 */
export function costsExportQuery(range: ResolvedRange): string {
  return costsQuery(range);
}

/**
 * sameSelection reports whether two picker selections describe the same window.
 *
 * Only the custom inputs matter for a custom range; text left behind in them is
 * ignored while a preset is selected.
 */
function sameSelection(left: DateRangeSelection, right: DateRangeSelection): boolean {
  if (left.preset !== right.preset) {
    return false;
  }
  if (left.preset !== 'custom') {
    return true;
  }
  return (
    left.customStart.trim() === right.customStart.trim() &&
    left.customEnd.trim() === right.customEnd.trim()
  );
}

/**
 * canExportSelection reports whether the CSV export would describe what the page
 * is currently showing.
 *
 * The loaded range lags the picker: it only catches up when a load completes, and
 * it never catches up at all for a selection that fails validation. Exporting in
 * that gap would silently write the previous range, so the export is offered only
 * while `selection` is the selection that produced the loaded range.
 */
export function canExportSelection(
  selection: DateRangeSelection,
  loaded: DateRangeSelection | null
): boolean {
  return loaded !== null && sameSelection(selection, loaded);
}

/** seriesQuery builds the agent-by-hour query behind the stacked time series. */
export function seriesQuery(range: ResolvedRange): string {
  return `group_by=${SERIES_GROUP_BY}&${instantParams(range)}`;
}
