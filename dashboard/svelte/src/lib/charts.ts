import { Chart as ChartJS, registerables } from 'chart.js';

let configured = false;

export const chartPalette: string[] = [
  '#3B82F6',
  '#8B5CF6',
  '#EC4899',
  '#F59E0B',
  '#22C55E',
  '#06B6D4',
  '#F43F5E',
  '#14B8A6'
];

export function configureCharts(): void {
  if (configured) {
    return;
  }

  ChartJS.register(...registerables);
  ChartJS.defaults.backgroundColor = 'transparent';
  ChartJS.defaults.borderColor = '#2A2A3C';
  ChartJS.defaults.color = '#8888A0';
  ChartJS.defaults.font.family = 'Inter, system-ui, sans-serif';
  ChartJS.defaults.plugins.legend.labels.color = '#8888A0';
  ChartJS.defaults.plugins.tooltip.backgroundColor = '#141420';
  ChartJS.defaults.plugins.tooltip.borderColor = '#2A2A3C';
  ChartJS.defaults.plugins.tooltip.borderWidth = 1;
  ChartJS.defaults.plugins.tooltip.titleColor = '#E4E4ED';
  ChartJS.defaults.plugins.tooltip.bodyColor = '#E4E4ED';

  configured = true;
}
