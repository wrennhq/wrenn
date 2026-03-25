<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { goto } from '$app/navigation';
	import { fetchStats, POLL_INTERVALS, type TimeRange, type StatsResponse } from '$lib/api/stats';

	const RANGES: TimeRange[] = ['5m', '1h', '6h', '24h', '30d'];

	type Props = { onlaunch?: () => void; launchDisabled?: boolean };
	let { onlaunch, launchDisabled = false }: Props = $props();

	let range = $state<TimeRange>('1h');
	let stats = $state<StatsResponse | null>(null);
	// loading is only true before the very first successful fetch; subsequent
	// polls update data silently to avoid blanking the cards and charts.
	let loading = $state(true);
	let error = $state<string | null>(null);

	let canvasRunning: HTMLCanvasElement;
	let canvasResource: HTMLCanvasElement;
	// eslint-disable-next-line @typescript-eslint/no-explicit-any
	let chartRunning: any = null;
	// eslint-disable-next-line @typescript-eslint/no-explicit-any
	let chartResource: any = null;

	let pollInterval: ReturnType<typeof setInterval> | null = null;

	async function load() {
		const result = await fetchStats(range);
		if (result.ok) {
			stats = result.data;
			error = null;
		} else {
			error = result.error;
		}
		// Set loading=false before updateCharts so cards always render even if
		// chart update throws (e.g. Chart.js not yet initialised on first tick).
		loading = false;
		updateCharts();
	}

	function updateCharts() {
		if (!stats) return;
		// Use Array.from to pass plain JS arrays to Chart.js — Svelte 5 $state
		// wraps arrays in reactive proxies which Chart.js can't iterate reliably.
		const labels = formatLabels(Array.from(stats.series.labels), range);
		if (chartRunning) {
			chartRunning.data.labels = labels;
			chartRunning.data.datasets[0].data = Array.from(stats.series.running);
			chartRunning.update();
		}
		if (chartResource) {
			chartResource.data.labels = labels;
			chartResource.data.datasets[0].data = Array.from(stats.series.vcpus);
			chartResource.data.datasets[1].data = Array.from(stats.series.memory_mb).map((mb) => +(mb / 1024).toFixed(2));
			chartResource.update();
		}
	}

	function formatLabels(labels: string[], r: TimeRange): string[] {
		return labels.map((iso) => {
			const d = new Date(iso);
			if (r === '5m' || r === '1h') {
				return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: r === '5m' ? '2-digit' : undefined });
			}
			if (r === '6h' || r === '24h') {
				return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
			}
			// 30d
			return d.toLocaleDateString([], { month: 'short', day: 'numeric' });
		});
	}

	function restartPolling() {
		if (pollInterval) clearInterval(pollInterval);
		load();
		pollInterval = setInterval(load, POLL_INTERVALS[range]);
	}

	function setRange(r: TimeRange) {
		range = r;
		goto(`?range=${r}`, { replaceState: true, noScroll: true, keepFocus: true });
		restartPolling();
	}

	// Chart colors (resolved from CSS vars, must match app.css)
	const C_ACCENT       = '#5e8c58';
	const C_ACCENT_FILL  = 'rgba(94,140,88,0.08)';
	const C_AMBER        = '#d4a73c';
	const C_AMBER_FILL   = 'rgba(212,167,60,0.06)';
	const C_GRID         = 'rgba(255,255,255,0.04)';
	const C_TICK         = '#454340';
	const FONT_MONO      = "'JetBrains Mono', monospace";

	const BASE_CHART_OPTIONS = {
		responsive: true,
		maintainAspectRatio: false,
		animation: false as const,
		interaction: { mode: 'index' as const, intersect: false },
		plugins: {
			legend: { display: false },
			tooltip: {
				backgroundColor: '#141817',
				borderColor: '#1f2321',
				borderWidth: 1,
				titleColor: '#454340',
				bodyColor: '#d4cfc8',
				titleFont: { family: FONT_MONO, size: 10 },
				bodyFont:  { family: FONT_MONO, size: 11 },
				padding: 10,
			},
		},
		scales: {
			x: {
				grid: { color: C_GRID },
				ticks: { color: C_TICK, font: { family: FONT_MONO, size: 10 }, maxTicksLimit: 6, maxRotation: 0 },
				border: { color: C_GRID },
			},
			y: {
				grid: { color: C_GRID },
				ticks: { color: C_TICK, font: { family: FONT_MONO, size: 10 }, precision: 0 },
				border: { color: C_GRID },
				beginAtZero: true,
			},
		},
	};

	onMount(async () => {
		// Read range from URL query param; fall back to '1h'.
		const urlRange = new URLSearchParams(window.location.search).get('range');
		if (urlRange && RANGES.includes(urlRange as TimeRange)) {
			range = urlRange as TimeRange;
		}

		const { Chart } = await import('chart.js/auto');

		chartRunning = new Chart(canvasRunning, {
			type: 'line',
			data: {
				labels: [],
				datasets: [{
					data: [],
					borderColor: C_ACCENT,
					backgroundColor: C_ACCENT_FILL,
					borderWidth: 1.5,
					fill: true,
					tension: 0,
					pointRadius: 0,
					pointHoverRadius: 4,
					pointHoverBackgroundColor: C_ACCENT,
				}],
			},
			options: BASE_CHART_OPTIONS,
		});

		chartResource = new Chart(canvasResource, {
			type: 'line',
			data: {
				labels: [],
				datasets: [
					{
						label: 'vCPUs',
						data: [],
						borderColor: C_ACCENT,
						backgroundColor: C_ACCENT_FILL,
						borderWidth: 1.5,
						fill: false,
						tension: 0,
						pointRadius: 0,
						pointHoverRadius: 4,
						pointHoverBackgroundColor: C_ACCENT,
						yAxisID: 'y',
					},
					{
						label: 'RAM (GB)',
						data: [],
						borderColor: C_AMBER,
						backgroundColor: C_AMBER_FILL,
						borderWidth: 1.5,
						fill: false,
						tension: 0,
						pointRadius: 0,
						pointHoverRadius: 4,
						pointHoverBackgroundColor: C_AMBER,
						yAxisID: 'yRam',
					},
				],
			},
			options: {
				...BASE_CHART_OPTIONS,
				plugins: {
					...BASE_CHART_OPTIONS.plugins,
					legend: {
						display: true,
						position: 'top' as const,
						align: 'end' as const,
						labels: {
							color: C_TICK,
							font: { family: FONT_MONO, size: 10 },
							boxWidth: 12,
							padding: 12,
						},
					},
					tooltip: {
						...BASE_CHART_OPTIONS.plugins.tooltip,
						callbacks: {
							label: (ctx: { dataset: { label?: string }; parsed: { y: number } }) => {
								if (ctx.dataset.label === 'RAM (GB)') {
									return ` RAM: ${ctx.parsed.y.toFixed(1)} GB`;
								}
								return ` vCPUs: ${ctx.parsed.y}`;
							},
						},
					},
				},
				scales: {
					...BASE_CHART_OPTIONS.scales,
					y: {
						...BASE_CHART_OPTIONS.scales.y,
						position: 'left' as const,
						title: { display: true, text: 'vCPUs', color: C_TICK, font: { family: FONT_MONO, size: 10 } },
					},
					yRam: {
						grid: { color: C_GRID },
						ticks: { color: C_TICK, font: { family: FONT_MONO, size: 10 } },
						border: { color: C_GRID },
						beginAtZero: true,
						position: 'right' as const,
						title: { display: true, text: 'GB', color: C_TICK, font: { family: FONT_MONO, size: 10 } },
					},
				},
			},
		});

		// Apply any data already loaded before charts were ready.
		updateCharts();

		restartPolling();
	});

	onDestroy(() => {
		if (pollInterval) clearInterval(pollInterval);
		chartRunning?.destroy();
		chartResource?.destroy();
	});

	function fmtGB(mb: number): string {
		return (mb / 1024).toFixed(1) + ' GB';
	}
</script>

<div class="p-8 space-y-5" style="animation: fadeUp 0.35s ease both">

	<!-- Header row: title + range selector + launch button -->
	<div class="flex items-center justify-between">
		<span class="text-meta font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]">Usage Statistics</span>
		<div class="flex items-center gap-3">
		<div class="flex overflow-hidden rounded-[var(--radius-button)] border border-[var(--color-border)]">
			{#each RANGES as r, i}
				<button
					onclick={() => setRange(r)}
					class="px-2.5 py-1 font-mono text-label transition-colors duration-150
						{range === r
							? 'bg-[var(--color-bg-5)] text-[var(--color-text-bright)]'
							: 'text-[var(--color-text-tertiary)] hover:text-[var(--color-text-secondary)]'}
						{i > 0 ? 'border-l border-[var(--color-border)]' : ''}"
				>
					{r}
				</button>
			{/each}
		</div>
		{#if onlaunch}
			<button
				onclick={onlaunch}
				disabled={launchDisabled}
				title={launchDisabled ? 'No active team — re-authenticate to create capsules' : undefined}
				class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-accent)] px-4 py-2 text-ui font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0 disabled:pointer-events-none disabled:opacity-40"
			>
				<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
					<line x1="12" y1="5" x2="12" y2="19" /><line x1="5" y1="12" x2="19" y2="12" />
				</svg>
				Launch Capsule
			</button>
		{/if}
		</div>
	</div>

	<!-- 4 stat cards -->
	<div class="flex overflow-hidden rounded-[var(--radius-card)] border border-[var(--color-border)]">

		<!-- Current Running -->
		<div class="flex-1 border-r border-[var(--color-border)] bg-[var(--color-bg-2)] px-5 py-5 transition-colors duration-150 hover:bg-[var(--color-bg-3)]">
			<div class="flex items-center gap-2">
				<span class="text-meta font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]">Running Now</span>
				{#if !loading}
					<span class="rounded-[3px] bg-[var(--color-accent-glow-mid)] px-1.5 py-0.5 text-badge font-semibold uppercase tracking-[0.04em] text-[var(--color-accent-mid)]">
						<span class="mr-0.5 inline-block h-[5px] w-[5px] rounded-full bg-[var(--color-accent)]" style="animation: wrenn-glow 2.5s ease-in-out infinite"></span>
						Live
					</span>
				{/if}
			</div>
			<div class="mt-1 font-serif text-[2.571rem] tracking-[-0.04em] text-[var(--color-text-bright)]">
				{loading ? '—' : (stats?.current.running_count ?? 0)}
			</div>
			<div class="mt-1 text-label text-[var(--color-text-tertiary)]">capsules</div>
		</div>

		<!-- Peak Running 30d -->
		<div class="flex-1 border-r border-[var(--color-border)] bg-[var(--color-bg-2)] px-5 py-5 transition-colors duration-150 hover:bg-[var(--color-bg-3)]">
			<span class="text-meta font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]">Peak Running</span>
			<div class="mt-1 font-serif text-[2.571rem] tracking-[-0.04em] text-[var(--color-text-bright)]">
				{loading ? '—' : (stats?.peaks.running_count ?? 0)}
			</div>
			<div class="mt-1 text-label text-[var(--color-text-tertiary)]">30-day max</div>
		</div>

		<!-- Peak CPU 30d -->
		<div class="flex-1 border-r border-[var(--color-border)] bg-[var(--color-bg-2)] px-5 py-5 transition-colors duration-150 hover:bg-[var(--color-bg-3)]">
			<span class="text-meta font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]">Peak CPU</span>
			<div class="mt-1 font-serif text-[2.571rem] tracking-[-0.04em] text-[var(--color-text-bright)]">
				{loading ? '—' : (stats?.peaks.vcpus ?? 0)}
			</div>
			<div class="mt-1 text-label text-[var(--color-text-tertiary)]">vCPUs reserved · 30d max</div>
		</div>

		<!-- Peak RAM 30d -->
		<div class="flex-1 bg-[var(--color-bg-2)] px-5 py-5 transition-colors duration-150 hover:bg-[var(--color-bg-3)]">
			<span class="text-meta font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]">Peak RAM</span>
			<div class="mt-1 font-serif text-[2.571rem] tracking-[-0.04em] text-[var(--color-text-bright)]">
				{loading ? '—' : fmtGB(stats?.peaks.memory_mb ?? 0)}
			</div>
			<div class="mt-1 text-label text-[var(--color-text-tertiary)]">reserved · 30d max</div>
		</div>

	</div>

	<!-- Error state -->
	{#if error}
		<div class="rounded-[var(--radius-card)] border border-[var(--color-red)]/20 bg-[var(--color-red)]/5 px-4 py-3 text-ui text-[var(--color-red)]/70">
			Failed to load stats: {error}
		</div>
	{/if}

	<!-- Running Capsules chart -->
	<div class="rounded-[var(--radius-card)] border border-[var(--color-border)] bg-[var(--color-bg-2)]">
		<div class="flex items-center justify-between px-5 pt-5 pb-3">
			<div>
				<div class="text-meta font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]">Running Capsules</div>
				<div class="mt-0.5 flex items-baseline gap-2">
					<span class="font-serif text-[2.143rem] tracking-[-0.04em] text-[var(--color-text-bright)]">
						{loading ? '—' : (stats?.current.running_count ?? 0)}
					</span>
					<span class="text-ui text-[var(--color-text-secondary)]">now</span>
				</div>
			</div>
		</div>
		{#if !loading && stats && stats.series.labels.length === 0}
			<div class="flex h-[200px] items-center justify-center text-ui text-[var(--color-text-muted)]">
				Metrics will appear here once capsules have run. First data arrives within 10 seconds.
			</div>
		{:else}
			<div class="relative h-[200px] px-5 pb-5">
				<canvas bind:this={canvasRunning}></canvas>
			</div>
		{/if}
	</div>

	<!-- Reserved CPU & RAM chart -->
	<div class="rounded-[var(--radius-card)] border border-[var(--color-border)] bg-[var(--color-bg-2)]">
		<div class="flex items-center justify-between px-5 pt-5 pb-3">
			<div>
				<div class="text-meta font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]">Reserved CPU & RAM</div>
				<div class="mt-0.5 flex items-baseline gap-2">
					<span class="font-serif text-[2.143rem] tracking-[-0.04em] text-[var(--color-text-bright)]">
						{loading ? '—' : (stats?.current.vcpus_reserved ?? 0)}
					</span>
					<span class="text-ui text-[var(--color-text-secondary)]">vCPUs</span>
					<span class="font-serif text-[2.143rem] tracking-[-0.04em] text-[var(--color-text-bright)]">
						{loading ? '—' : fmtGB(stats?.current.memory_mb_reserved ?? 0)}
					</span>
					<span class="text-ui text-[var(--color-text-secondary)]">RAM</span>
				</div>
			</div>
		</div>
		{#if !loading && stats && stats.series.labels.length === 0}
			<div class="flex h-[200px] items-center justify-center text-ui text-[var(--color-text-muted)]">
				Metrics will appear here once capsules have run. First data arrives within 10 seconds.
			</div>
		{:else}
			<div class="relative h-[200px] px-5 pb-5">
				<canvas bind:this={canvasResource}></canvas>
			</div>
		{/if}
	</div>

</div>
