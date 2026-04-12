<script lang="ts">
	import { onMount, onDestroy, tick } from 'svelte';
	import {
		fetchCapsuleMetrics,
		METRIC_RANGES,
		METRIC_POLL_INTERVALS,
		type MetricRange,
		type MetricPoint
	} from '$lib/api/metrics';

	type Props = {
		capsuleId: string;
		/** Whether the capsule is in a state that supports metrics */
		available: boolean;
		/** Initial range selection */
		initialRange?: MetricRange;
		/** API base path for fetching metrics */
		apiBasePath?: string;
		/** Layout: 'full' shows padded cards with gap, 'compact' shows borderless stacked charts */
		layout?: 'full' | 'compact';
	};

	let { capsuleId, available, initialRange = '10m', apiBasePath = '/api/v1/capsules', layout = 'full' }: Props = $props();

	let range = $state<MetricRange>(initialRange);
	let points = $state<MetricPoint[]>([]);
	let metricsLoading = $state(true);
	let metricsError = $state<string | null>(null);

	let canvasCpu = $state<HTMLCanvasElement | undefined>(undefined);
	let canvasRam = $state<HTMLCanvasElement | undefined>(undefined);
	// eslint-disable-next-line @typescript-eslint/no-explicit-any
	let chartCpu: any = null;
	// eslint-disable-next-line @typescript-eslint/no-explicit-any
	let chartRam: any = null;
	// eslint-disable-next-line @typescript-eslint/no-explicit-any
	let ChartJS = $state<any>(null);
	let pollInterval: ReturnType<typeof setInterval> | null = null;
	let lastDataKey = '';
	let visibilityHandler: (() => void) | null = null;

	const latestCpu = $derived<number | null>(
		points.length > 0 ? points[points.length - 1].cpu_pct : null
	);
	const latestRamMB = $derived<number | null>(
		points.length > 0 ? points[points.length - 1].mem_bytes / 1_048_576 : null
	);

	async function loadMetrics() {
		if (!available) return;
		const result = await fetchCapsuleMetrics(capsuleId, range, apiBasePath);
		if (result.ok) {
			points = result.data.points;
			metricsError = null;
		} else {
			metricsError = result.error;
		}
		metricsLoading = false;
		updateCharts();
	}

	function smooth(data: number[], window: number): number[] {
		if (window <= 1) return data;
		const out: number[] = [];
		for (let i = 0; i < data.length; i++) {
			const start = Math.max(0, i - Math.floor(window / 2));
			const end = Math.min(data.length, i + Math.ceil(window / 2));
			let sum = 0;
			for (let j = start; j < end; j++) sum += data[j];
			out.push(+(sum / (end - start)).toFixed(2));
		}
		return out;
	}

	function smoothWindow(count: number): number {
		if (count < 60) return 1;
		if (count < 200) return 3;
		if (count < 600) return 5;
		return 9;
	}

	function updateCharts() {
		if (!points.length) return;
		const key = `${points.length}:${points.at(-1)?.timestamp_unix ?? ''}`;
		if (key === lastDataKey) return;
		lastDataKey = key;
		const labels = points.map((p) => {
			const d = new Date(p.timestamp_unix * 1000);
			if (range === '5m' || range === '10m') {
				return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
			}
			return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
		});
		const w = smoothWindow(points.length);
		if (chartCpu) {
			chartCpu.data.labels = labels;
			chartCpu.data.datasets[0].data = smooth(points.map((p) => +p.cpu_pct.toFixed(2)), w);
			chartCpu.update();
		}
		if (chartRam) {
			chartRam.data.labels = labels;
			chartRam.data.datasets[0].data = smooth(points.map((p) => +(p.mem_bytes / 1_048_576).toFixed(1)), w);
			chartRam.update();
		}
	}

	function setRange(r: MetricRange) {
		range = r;
		lastDataKey = '';
		metricsLoading = true;
		restartPolling();
	}

	function stopPolling() {
		if (pollInterval) { clearInterval(pollInterval); pollInterval = null; }
	}

	function restartPolling() {
		stopPolling();
		loadMetrics();
		pollInterval = setInterval(loadMetrics, METRIC_POLL_INTERVALS[range]);
	}

	const C_BLUE       = '#5a9fd4';
	const C_BLUE_FILL  = 'rgba(90,159,212,0.11)';
	const C_AMBER      = '#d4a73c';
	const C_AMBER_FILL = 'rgba(212,167,60,0.11)';
	const C_GRID       = 'rgba(255,255,255,0.05)';
	const C_TICK       = '#635f5c';
	const FONT_MONO    = "'JetBrains Mono', monospace";

	const BASE_CHART_OPTIONS = {
		responsive: true,
		maintainAspectRatio: false,
		animation: false as const,
		interaction: { mode: 'index' as const, intersect: false },
		plugins: {
			legend: { display: false },
			tooltip: {
				backgroundColor: '#111412',
				borderColor: '#1f2321',
				borderWidth: 1,
				titleColor: '#454340',
				bodyColor: '#d4cfc8',
				titleFont: { family: FONT_MONO, size: 10 },
				bodyFont: { family: FONT_MONO, size: 11 },
				padding: 10,
				caretSize: 4,
			},
		},
		scales: {
			x: {
				grid: { color: C_GRID },
				ticks: { color: C_TICK, font: { family: FONT_MONO, size: 10 }, maxTicksLimit: 8, maxRotation: 0 },
				border: { color: C_GRID },
			},
			y: {
				grid: { color: C_GRID },
				ticks: { color: C_TICK, font: { family: FONT_MONO, size: 10 } },
				border: { color: C_GRID },
				beginAtZero: true,
			},
		},
	};

	function initCharts() {
		if (!ChartJS || !canvasCpu || !canvasRam) return;
		chartCpu?.destroy();
		chartRam?.destroy();

		chartCpu = new ChartJS(canvasCpu, {
			type: 'line',
			data: {
				labels: [],
				datasets: [{
					data: [], borderColor: C_BLUE, backgroundColor: C_BLUE_FILL,
					borderWidth: 2, fill: true, tension: 0, pointRadius: 0,
					pointHoverRadius: 4, pointHoverBackgroundColor: C_BLUE,
				}],
			},
			options: {
				...BASE_CHART_OPTIONS,
				plugins: { ...BASE_CHART_OPTIONS.plugins, tooltip: { ...BASE_CHART_OPTIONS.plugins.tooltip,
					// eslint-disable-next-line @typescript-eslint/no-explicit-any
					callbacks: { label: (ctx: any) => ` ${ctx.parsed.y.toFixed(1)}%` },
				}},
				scales: { ...BASE_CHART_OPTIONS.scales, y: { ...BASE_CHART_OPTIONS.scales.y,
					ticks: { ...BASE_CHART_OPTIONS.scales.y.ticks, callback: (v: string | number) => `${+v}%` },
				}},
			},
		});

		chartRam = new ChartJS(canvasRam, {
			type: 'line',
			data: {
				labels: [],
				datasets: [{
					data: [], borderColor: C_AMBER, backgroundColor: C_AMBER_FILL,
					borderWidth: 2, fill: true, tension: 0, pointRadius: 0,
					pointHoverRadius: 4, pointHoverBackgroundColor: C_AMBER,
				}],
			},
			options: {
				...BASE_CHART_OPTIONS,
				plugins: { ...BASE_CHART_OPTIONS.plugins, tooltip: { ...BASE_CHART_OPTIONS.plugins.tooltip,
					// eslint-disable-next-line @typescript-eslint/no-explicit-any
					callbacks: { label: (ctx: any) => ` ${ctx.parsed.y.toFixed(0)} MB` },
				}},
				scales: { ...BASE_CHART_OPTIONS.scales, y: { ...BASE_CHART_OPTIONS.scales.y,
					ticks: { ...BASE_CHART_OPTIONS.scales.y.ticks, callback: (v: string | number) => `${+v} MB` },
				}},
			},
		});

		updateCharts();
	}

	$effect(() => {
		if (!ChartJS || !available) return;
		tick().then(() => {
			if (canvasCpu && canvasRam) {
				initCharts();
				restartPolling();
			}
		});
		return () => {
			stopPolling();
			chartCpu?.destroy(); chartCpu = null;
			chartRam?.destroy(); chartRam = null;
		};
	});

	onMount(async () => {
		if (!available) return;
		const mod = await import('chart.js/auto');
		ChartJS = mod.Chart;

		visibilityHandler = () => {
			if (document.hidden) {
				stopPolling();
			} else if (available) {
				restartPolling();
			}
		};
		document.addEventListener('visibilitychange', visibilityHandler);
	});

	onDestroy(() => {
		stopPolling();
		if (visibilityHandler) document.removeEventListener('visibilitychange', visibilityHandler);
		chartCpu?.destroy();
		chartRam?.destroy();
	});
</script>

<style>
	.metric-val {
		transition: color 0.3s ease;
	}
</style>

<div class="flex flex-1 flex-col min-h-0">
	<!-- Controls row -->
	<div class="flex shrink-0 items-center justify-between {layout === 'full' ? 'px-0 pb-5' : 'border-b border-[var(--color-border)] bg-[var(--color-bg-1)] px-5 py-2'}">
		{#if layout === 'full'}
			{#if !metricsLoading}
				<span class="flex items-center gap-1.5 rounded-[3px] border border-[var(--color-accent)]/25 bg-[var(--color-accent-glow-mid)] px-2 py-1 text-badge font-semibold uppercase tracking-[0.05em] text-[var(--color-accent-mid)]">
					<span class="h-[5px] w-[5px] rounded-full bg-[var(--color-accent)]" style="animation: wrenn-glow 2.5s ease-in-out infinite"></span>
					Live
				</span>
			{:else}
				<div></div>
			{/if}
		{:else}
			<div></div>
		{/if}

		<div class="flex overflow-hidden rounded-[var(--radius-button)] border border-[var(--color-border)]">
			{#each METRIC_RANGES as r, i}
				<button
					onclick={() => setRange(r)}
					class="px-3 py-1.5 font-mono text-label transition-colors duration-150
						{range === r
							? 'bg-[var(--color-bg-5)] text-[var(--color-text-bright)]'
							: 'text-[var(--color-text-tertiary)] hover:bg-[var(--color-bg-3)] hover:text-[var(--color-text-secondary)]'}
						{i > 0 ? 'border-l border-[var(--color-border)]' : ''}"
				>
					{r}
				</button>
			{/each}
		</div>
	</div>

	{#if metricsError}
		<div class="flex shrink-0 items-center gap-3 rounded-[var(--radius-card)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/8 px-4 py-3 {layout === 'full' ? 'mb-5' : 'mx-5 my-3'}">
			<svg class="shrink-0 text-[var(--color-red)]" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
				<circle cx="12" cy="12" r="10" /><line x1="12" y1="8" x2="12" y2="12" /><line x1="12" y1="16" x2="12.01" y2="16" />
			</svg>
			<span class="text-ui text-[var(--color-red)]">Could not load metrics: {metricsError}. Will retry automatically.</span>
		</div>
	{/if}

	<!-- Charts — stacked, each grows to fill half -->
	<div class="flex flex-1 flex-col min-h-0 {layout === 'full' ? 'gap-5' : 'divide-y divide-[var(--color-border)]'}">

		<!-- CPU Usage -->
		<div class="flex flex-1 flex-col min-h-0 {layout === 'full' ? 'rounded-[var(--radius-card)] border border-[var(--color-border)] bg-[var(--color-bg-2)]' : 'bg-[var(--color-bg-1)]'}">
			<div class="flex shrink-0 items-center justify-between {layout === 'full' ? 'border-b border-[var(--color-border)] px-6 py-4' : 'px-5 py-2'}">
				<div class="flex items-center gap-2">
					<span class="h-2 w-2 shrink-0 rounded-full" style="background: {C_BLUE}"></span>
					<span class="text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">CPU Usage</span>
				</div>
				{#if latestCpu !== null}
					<div class="flex items-baseline gap-1">
						<span class="metric-val font-serif {layout === 'full' ? 'text-[2.571rem]' : 'text-heading'} leading-none tracking-[-0.04em] text-[var(--color-text-bright)]">{latestCpu.toFixed(1)}</span>
						<span class="font-mono {layout === 'full' ? 'text-label' : 'text-badge'} text-[var(--color-text-muted)]">%</span>
					</div>
				{:else if metricsLoading}
					<span class="font-serif {layout === 'full' ? 'text-[2.571rem]' : 'text-heading'} leading-none text-[var(--color-text-muted)]">—</span>
				{/if}
			</div>
			<div class="relative flex-1 min-h-0 {layout === 'full' ? 'min-h-[180px] px-5 pb-5 pt-3' : 'px-4 pb-3 pt-1'}">
				<canvas bind:this={canvasCpu}></canvas>
			</div>
		</div>

		<!-- RAM Usage -->
		<div class="flex flex-1 flex-col min-h-0 {layout === 'full' ? 'rounded-[var(--radius-card)] border border-[var(--color-border)] bg-[var(--color-bg-2)]' : 'bg-[var(--color-bg-1)]'}">
			<div class="flex shrink-0 items-center justify-between {layout === 'full' ? 'border-b border-[var(--color-border)] px-6 py-4' : 'px-5 py-2'}">
				<div class="flex items-center gap-2">
					<span class="h-2 w-2 shrink-0 rounded-full" style="background: {C_AMBER}"></span>
					<span class="text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">RAM Usage</span>
				</div>
				{#if latestRamMB !== null}
					<div class="flex items-baseline gap-1">
						<span class="metric-val font-serif {layout === 'full' ? 'text-[2.571rem]' : 'text-heading'} leading-none tracking-[-0.04em] text-[var(--color-text-bright)]">{latestRamMB.toFixed(0)}</span>
						<span class="font-mono {layout === 'full' ? 'text-label' : 'text-badge'} text-[var(--color-text-muted)]">MB</span>
					</div>
				{:else if metricsLoading}
					<span class="font-serif {layout === 'full' ? 'text-[2.571rem]' : 'text-heading'} leading-none text-[var(--color-text-muted)]">—</span>
				{/if}
			</div>
			<div class="relative flex-1 min-h-0 {layout === 'full' ? 'min-h-[180px] px-5 pb-5 pt-3' : 'px-4 pb-3 pt-1'}">
				<canvas bind:this={canvasRam}></canvas>
			</div>
		</div>

	</div>
</div>
