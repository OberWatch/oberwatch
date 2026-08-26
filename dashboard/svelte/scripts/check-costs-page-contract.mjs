import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

function read(relativePath) {
  return readFileSync(new URL(relativePath, import.meta.url), 'utf8');
}

const picker = read('../src/lib/components/DateRangePicker.svelte');
const lineChart = read('../src/lib/components/LineChart.svelte');
const costsPage = read('../src/routes/costs/+page.svelte');
const costsLoader = read('../src/lib/costsLoader.ts');

// --- Date range picker: real custom inputs, labelled and validated -----------

const dateInputs = picker.match(/type="date"/g) ?? [];
assert.equal(dateInputs.length, 2, 'DateRangePicker must render a start and an end date input');
assert.match(picker, /customStart/, 'DateRangePicker must bind a custom start value');
assert.match(picker, /customEnd/, 'DateRangePicker must bind a custom end value');
assert.match(picker, /<label[\s>]/, 'Custom date inputs must be labelled');
assert.match(
  picker,
  /\berror\b/,
  'DateRangePicker must be able to show a validation message next to the inputs'
);
assert.match(
  picker,
  /aria-invalid/,
  'An invalid custom range must be exposed to assistive technology'
);

// --- Line chart: stacking delegated to the tested option builder -------------

assert.match(
  lineChart,
  /import\s*\{[^}]*lineChartOptions[^}]*\}\s*from\s*'\$lib\/chartOptions'/,
  'LineChart must build its options with lineChartOptions'
);
assert.match(
  lineChart,
  /import\s*\{[^}]*lineDatasetStyle[^}]*\}\s*from\s*'\$lib\/chartOptions'/,
  'LineChart must style datasets with lineDatasetStyle'
);
assert.match(lineChart, /\bstacked\b/, 'LineChart must accept a stacked prop');
assert.doesNotMatch(
  lineChart,
  /scales:\s*\{/,
  'LineChart must not hand-roll scale options that bypass the tested builder'
);

// --- Costs page: one resolved range drives everything -----------------------

assert.match(
  costsPage,
  /import\s*\{[^}]*createCostsLoader[^}]*\}\s*from\s*'\$lib\/costsLoader'/,
  'Costs page must load through the tested loader so stale responses are dropped'
);
for (const helper of ['resolveRange', 'costsQuery', 'seriesQuery']) {
  assert.ok(
    costsLoader.includes(helper),
    `The loader must build its window with ${helper} from $lib/dateRange`
  );
}
assert.ok(
  costsPage.includes('costsExportQuery'),
  'Costs page must build its CSV query through $lib/dateRange'
);
for (const helper of ['buildStackedSeries', 'totalsByAgent', 'totalsByModel']) {
  assert.ok(costsPage.includes(helper), `Costs page must shape data with ${helper}`);
}

// A superseded response must be discarded rather than applied to newer state.
assert.match(
  costsPage,
  /'stale'/,
  'Costs page must handle the stale outcome from the loader'
);
assert.match(
  costsPage,
  /status\s*===\s*'stale'[\s\S]{0,120}?return/,
  'A stale load must return without touching page state'
);

assert.doesNotMatch(
  costsPage,
  /function\s+(fromForRange|queryForRange|toHourBucket)\b/,
  'Ad-hoc range and bucket helpers must be replaced by the tested modules'
);

// The range must be resolved once per load. A second `new Date()` anywhere else
// means the export or a chart could describe a different window than the table.
const nowConstructions = costsPage.match(/new Date\(\s*\)/g) ?? [];
assert.equal(
  nowConstructions.length,
  1,
  `Costs page must read the clock exactly once per load, found ${nowConstructions.length}`
);

assert.match(
  costsPage,
  /activeRange/,
  'Costs page must keep the resolved range in state so every consumer shares it'
);
assert.match(
  costsPage,
  /costsExportQuery\(\s*activeRange\b/,
  'CSV export must reuse the stored resolved range rather than recomputing it'
);

// The loaded range lags the picker while a load is in flight, and never catches
// up for an invalid custom range. Exporting in that gap wrote the previous range.
assert.match(
  costsPage,
  /canExportSelection\(/,
  'Costs page must gate the export on the selection that produced the loaded range'
);
assert.match(
  costsPage,
  /activeSelection/,
  'Costs page must remember which selection produced the loaded range'
);
assert.match(
  costsPage,
  /disabled=\{!\s*exportEnabled\s*\}/,
  'The Export button must be disabled when it would write a different range'
);
assert.match(
  costsPage,
  /function\s+exportCSV[\s\S]{0,200}?if\s*\(!exportEnabled/,
  'exportCSV must also refuse to run when the export is not current'
);
assert.match(
  costsLoader,
  /seriesQuery\(\s*range\b/,
  'The stacked series must be fetched for the same resolved range'
);
// Day buckets are folded on the client, so the series is always fetched hourly.
assert.match(
  costsLoader,
  /seriesQuery/,
  'The time series must use the agent-preserving grouping helper'
);
assert.doesNotMatch(
  costsLoader,
  /agent_day/,
  'agent_day is not calendar-correct and must not be requested'
);

// --- Costs page: retry preserves filters, empty and error states stay usable --

assert.match(
  costsPage,
  /onclick=\{\s*retry\s*\}|onclick=\{\(\)\s*=>\s*retry\(\)\}/,
  'The error state must offer a Retry action'
);
assert.match(
  costsPage,
  /function\s+retry\s*\([^)]*\)[\s\S]{0,400}?loadCosts\(\s*selection\s*\)/,
  'Retry must reload with the current selection so filters are preserved'
);
assert.match(costsPage, /Retry/, 'The error state must be labelled Retry');
assert.match(
  costsPage,
  /No cost data/,
  'The empty state must explain that the range has no data'
);
assert.match(
  costsPage,
  /<LineChart[^>]*stacked/,
  'The costs page must ask LineChart for a stacked series'
);

// --- Stacked series accessibility -------------------------------------------

assert.match(
  lineChart,
  /ariaLabel/,
  'LineChart must accept an accessible label for its canvas'
);
// A canvas may not carry role="img". The wrapper is the labelled image instead,
// and the canvas itself is hidden so the label is not announced twice.
assert.doesNotMatch(
  lineChart,
  /<canvas[^>]*role=/,
  'A canvas cannot take an ARIA role'
);
assert.match(
  lineChart,
  /<div[^>]*role="img"[^>]*aria-label=\{ariaLabel\}|<div[^>]*aria-label=\{ariaLabel\}[^>]*role="img"/,
  'The chart wrapper must be the labelled image'
);
assert.match(
  lineChart,
  /<canvas[^>]*aria-hidden="true"/,
  'The canvas must be hidden from assistive technology'
);
assert.match(
  costsPage,
  /describeStackedSeries/,
  'The stacked chart must be labelled with a real description of what it plots'
);
assert.match(
  costsPage,
  /<LineChart[^>]*ariaLabel=/,
  'The costs page must pass the description to the chart'
);
assert.match(
  costsPage,
  /<summary[\s>]/,
  'The series must have a keyboard-accessible textual alternative'
);
assert.match(
  costsPage,
  /<table[\s>][\s\S]*?<caption[\s>]/,
  'The textual alternative must be a captioned table of the same numbers'
);

console.log('Costs page range, stacked-series and retry contract checks passed.');
