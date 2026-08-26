/**
 * Chart.js option builders.
 *
 * Kept separate from `charts.ts` so the option shapes can be asserted without
 * loading the Chart.js runtime, which needs a canvas.
 */

import type { ChartOptions } from 'chart.js';

const AXIS_TICK_COLOR = '#8888A0';
const AXIS_GRID_COLOR = '#2A2A3C';
const FALLBACK_SERIES_COLOR = '#3B82F6';

/** LineChartConfig selects between overlaid lines and a stacked band chart. */
export interface LineChartConfig {
  stacked: boolean;
}

/** LineDatasetStyle is the per-series styling applied on top of the caller's dataset. */
export interface LineDatasetStyle {
  fill: boolean;
  borderColor: string;
  backgroundColor: string;
  tension: number;
  borderWidth: number;
  pointRadius: number;
  pointHoverRadius: number;
}

/**
 * lineChartOptions builds the line-chart options. When `stacked` is set both
 * axes stack, so the bands add up to the bucket total instead of overlapping and
 * hiding each other.
 */
export function lineChartOptions(config: LineChartConfig): ChartOptions<'line'> {
  return {
    responsive: true,
    maintainAspectRatio: false,
    interaction: {
      mode: 'index',
      intersect: false
    },
    scales: {
      x: {
        stacked: config.stacked,
        ticks: {
          color: AXIS_TICK_COLOR
        },
        grid: {
          color: AXIS_GRID_COLOR
        }
      },
      y: {
        stacked: config.stacked,
        beginAtZero: true,
        ticks: {
          color: AXIS_TICK_COLOR
        },
        grid: {
          color: AXIS_GRID_COLOR
        }
      }
    },
    plugins: {
      legend: {
        labels: {
          color: AXIS_TICK_COLOR
        }
      }
    }
  };
}

/**
 * lineDatasetStyle picks the styling for the dataset at `index`. Stacked series
 * are filled so the bands read as areas; overlaid series stay as plain lines.
 */
export function lineDatasetStyle(
  config: LineChartConfig,
  index: number,
  palette: string[]
): LineDatasetStyle {
  const color =
    palette.length === 0 ? FALLBACK_SERIES_COLOR : palette[index % palette.length];

  return {
    fill: config.stacked,
    borderColor: color,
    backgroundColor: color,
    tension: 0.35,
    borderWidth: 2,
    pointRadius: 2,
    pointHoverRadius: 4
  };
}
