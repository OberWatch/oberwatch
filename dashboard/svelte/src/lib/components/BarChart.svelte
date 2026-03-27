<script lang="ts">
  import { onMount } from 'svelte';
  import { Chart, type ChartOptions } from 'chart.js';
  import { chartPalette, configureCharts } from '$lib/charts';

  let {
    labels,
    values,
    colors,
    height = 320
  }: {
    labels: string[];
    values: number[];
    colors?: string[];
    height?: number;
  } = $props();

  let canvasEl: HTMLCanvasElement | null = null;
  let chart: Chart<'bar', number[], string> | null = null;

  const effectiveColors = $derived.by<string[]>(() => {
    const selected = colors && colors.length > 0 ? colors : chartPalette;
    return labels.map((_, index) => selected[index % selected.length]);
  });

  function buildData() {
    return {
      labels,
      datasets: [
        {
          label: 'Cost',
          data: values,
          borderWidth: 1,
          borderColor: effectiveColors,
          backgroundColor: effectiveColors
        }
      ]
    };
  }

  const options: ChartOptions<'bar'> = {
    responsive: true,
    maintainAspectRatio: false,
    indexAxis: 'y',
    scales: {
      x: {
        beginAtZero: true,
        ticks: {
          color: '#8888A0'
        },
        grid: {
          color: '#2A2A3C'
        }
      },
      y: {
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
        display: false
      }
    }
  };

  onMount(() => {
    configureCharts();
    if (!canvasEl) {
      return;
    }

    chart = new Chart(canvasEl, {
      type: 'bar',
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
