<script lang="ts">
  import { onMount } from 'svelte';
  import { Chart, type ChartDataset, type ChartOptions } from 'chart.js';
  import { chartPalette, configureCharts } from '$lib/charts';

  let {
    labels,
    datasets,
    height = 280
  }: {
    labels: string[];
    datasets: ChartDataset<'line', number[]>[];
    height?: number;
  } = $props();

  let canvasEl: HTMLCanvasElement | null = null;
  let chart: Chart<'line', number[], string> | null = null;

  function buildData() {
    return {
      labels,
      datasets: datasets.map((dataset, index) => ({
        tension: 0.35,
        fill: false,
        borderWidth: 2,
        pointRadius: 2,
        pointHoverRadius: 4,
        borderColor: dataset.borderColor ?? chartPalette[index % chartPalette.length],
        backgroundColor: dataset.backgroundColor ?? chartPalette[index % chartPalette.length],
        ...dataset
      }))
    };
  }

  const options: ChartOptions<'line'> = {
    responsive: true,
    maintainAspectRatio: false,
    interaction: {
      mode: 'index',
      intersect: false
    },
    scales: {
      x: {
        ticks: {
          color: '#8888A0'
        },
        grid: {
          color: '#2A2A3C'
        }
      },
      y: {
        beginAtZero: true,
        ticks: {
          color: '#8888A0'
        },
        grid: {
          color: '#2A2A3C'
        }
      }
    },
    plugins: {
      legend: {
        labels: {
          color: '#8888A0'
        }
      }
    }
  };

  onMount(() => {
    configureCharts();
    if (!canvasEl) {
      return;
    }

    chart = new Chart(canvasEl, {
      type: 'line',
      data: buildData(),
      options
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
    chart.options = options;
    chart.update();
  });
</script>

<div class="w-full rounded-lg border border-border-default bg-surface p-4" style={`height: ${height}px`}>
  <canvas bind:this={canvasEl}></canvas>
</div>
