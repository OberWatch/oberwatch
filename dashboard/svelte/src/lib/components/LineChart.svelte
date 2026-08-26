<script lang="ts">
  import { onMount } from 'svelte';
  import { Chart, type ChartDataset } from 'chart.js';
  import { chartPalette, configureCharts } from '$lib/charts';
  import { lineChartOptions, lineDatasetStyle } from '$lib/chartOptions';

  let {
    labels,
    datasets,
    stacked = false,
    ariaLabel = 'Line chart',
    height = 280
  }: {
    labels: string[];
    datasets: ChartDataset<'line', number[]>[];
    stacked?: boolean;
    // A canvas is opaque to assistive technology, so callers must say what the
    // chart plots. Pair it with a textual alternative for the values themselves.
    ariaLabel?: string;
    height?: number;
  } = $props();

  let canvasEl: HTMLCanvasElement | null = null;
  let chart: Chart<'line', number[], string> | null = null;

  function buildData() {
    return {
      labels,
      datasets: datasets.map((dataset, index) => ({
        ...lineDatasetStyle({ stacked }, index, chartPalette),
        ...dataset
      }))
    };
  }

  onMount(() => {
    configureCharts();
    if (!canvasEl) {
      return;
    }

    chart = new Chart(canvasEl, {
      type: 'line',
      data: buildData(),
      options: lineChartOptions({ stacked })
    });

    return () => {
      chart?.destroy();
      chart = null;
    };
  });

  $effect(() => {
    if (!chart) {
      return;
    }

    chart.data = buildData();
    chart.options = lineChartOptions({ stacked });
    chart.update();
  });
</script>

<!--
  A canvas cannot carry an ARIA role, so the wrapper is the labelled image and the
  canvas is hidden to keep the label from being announced twice.
-->
<div
  class="w-full rounded-lg border border-border-default bg-surface p-4"
  style={`height: ${height}px`}
  role="img"
  aria-label={ariaLabel}
>
  <canvas bind:this={canvasEl} aria-hidden="true"></canvas>
</div>
