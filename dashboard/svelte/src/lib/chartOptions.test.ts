import { test } from 'node:test';
import assert from 'node:assert/strict';

import type { ChartOptions } from 'chart.js';

import { lineChartOptions, lineDatasetStyle } from './chartOptions.ts';

/**
 * Chart.js types the scale options as a union over every scale kind, so read the
 * two flags under test through a narrow structural view.
 */
interface AxisView {
  stacked?: boolean;
  beginAtZero?: boolean;
}

function axis(options: ChartOptions<'line'>, key: 'x' | 'y'): AxisView {
  return (options.scales?.[key] ?? {}) as AxisView;
}

test('lineChartOptions stacks both axes only when stacking is requested', () => {
  const stacked = lineChartOptions({ stacked: true });
  assert.equal(axis(stacked, 'y').stacked, true, 'y axis must stack so the bands add up');
  assert.equal(axis(stacked, 'x').stacked, true, 'x axis must stack so buckets align');

  const overlaid = lineChartOptions({ stacked: false });
  assert.notEqual(axis(overlaid, 'y').stacked, true, 'unstacked charts must not stack y');
  assert.notEqual(axis(overlaid, 'x').stacked, true, 'unstacked charts must not stack x');
});

test('lineChartOptions keeps the shared axis and tooltip behaviour', () => {
  for (const stacked of [true, false]) {
    const options = lineChartOptions({ stacked });
    assert.equal(options.responsive, true, 'charts resize with their container');
    assert.equal(options.maintainAspectRatio, false);
    assert.equal(options.interaction?.mode, 'index', 'hovering compares every agent in a bucket');
    assert.equal(options.interaction?.intersect, false);
    assert.equal(axis(options, 'y').beginAtZero, true, 'cost axes start at zero');
  }
});

test('lineDatasetStyle fills stacked bands and cycles the palette', () => {
  const palette = ['#111111', '#222222'];

  const cases: Array<{
    name: string;
    stacked: boolean;
    index: number;
    wantFill: boolean;
    wantColor: string;
  }> = [
    { name: 'first stacked band', stacked: true, index: 0, wantFill: true, wantColor: '#111111' },
    { name: 'second stacked band', stacked: true, index: 1, wantFill: true, wantColor: '#222222' },
    { name: 'palette wraps around', stacked: true, index: 2, wantFill: true, wantColor: '#111111' },
    { name: 'unstacked lines are not filled', stacked: false, index: 0, wantFill: false, wantColor: '#111111' }
  ];

  for (const testCase of cases) {
    const style = lineDatasetStyle(
      { stacked: testCase.stacked },
      testCase.index,
      palette
    );
    assert.equal(style.fill, testCase.wantFill, `${testCase.name}: fill`);
    assert.equal(style.borderColor, testCase.wantColor, `${testCase.name}: border colour`);
    assert.equal(style.backgroundColor, testCase.wantColor, `${testCase.name}: background colour`);
  }
});

test('lineDatasetStyle survives an empty palette without producing undefined colours', () => {
  const style = lineDatasetStyle({ stacked: true }, 0, []);
  assert.equal(typeof style.borderColor, 'string');
  assert.notEqual(style.borderColor, '');
});
