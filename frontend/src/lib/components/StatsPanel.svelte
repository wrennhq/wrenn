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
	const C_BLUE         = '#5a9fd4';
	const C_BLUE_FILL    = 'rgba(90,159,212,0.07)';
	const C_AMBER        = '#d4a73c';
	const C_AMBER_FILL   = 'rgba(212,167,60,0.07)';
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
						borderColor: C_BLUE,
						backgroundColor: C_BLUE_FILL,
						borderWidth: 1.5,
						fill: false,
						tension: 0,
						pointRadius: 0,
						pointHoverRadius: 4,
						pointHoverBackgroundColor: C_BLUE,
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

<div class="flex flex-col gap-7 p-8" style="min-height: calc(100dvh - 200px); animation: fadeUp 0.35s ease both">

	<!-- Header: title + controls -->
	<div class="flex items-end justify-between">
		<div>
			<div class="flex items-center gap-2.5">
				<h2 class="font-serif text-heading tracking-[-0.02em] text-[var(--color-text-bright)]">Usage Statistics</h2>
				{#if !loading}
					<span class="flex items-center gap-1 rounded-[3px] border border-[var(--color-accent)]/25 bg-[var(--color-accent-glow-mid)] px-1.5 py-0.5 text-badge font-semibold uppercase tracking-[0.05em] text-[var(--color-accent-mid)]">
						<span class="h-[5px] w-[5px] rounded-full bg-[var(--color-accent)]" style="animation: wrenn-glow 2.5s ease-in-out infinite"></span>
						Live
					</span>
				{/if}
			</div>
			<p class="mt-1 text-ui text-[var(--color-text-tertiary)]">Resource consumption across all capsules.</p>
		</div>
		<div class="flex items-center gap-3">
			<!-- Range selector -->
			<div class="flex overflow-hidden rounded-[var(--radius-button)] border border-[var(--color-border)]">
				{#each RANGES as r, i}
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

	<!-- Stat cards: 3 paired cards (now / 30d peak) -->
	<div class="grid grid-cols-3 overflow-hidden rounded-[var(--radius-card)] border border-[var(--color-border)]">

		<!-- Running capsules -->
		<div class="border-r border-[var(--color-border)]" style="box-shadow: inset 3px 0 0 var(--color-accent)">
			<div class="flex items-center gap-2 border-b border-[var(--color-border)] bg-[var(--color-bg-3)] px-6 py-3">
				<span class="h-[6px] w-[6px] rounded-full bg-[var(--color-accent)]"></span>
				<span class="text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Running Capsules</span>
			</div>
			<div class="grid grid-cols-2 divide-x divide-[var(--color-border)]">
				<div class="bg-[var(--color-bg-3)] px-6 py-5 transition-colors duration-150 hover:bg-[var(--color-bg-4)]">
					<div class="text-label text-[var(--color-text-muted)]">Now</div>
					<div class="mt-1.5 font-serif text-[2.571rem] leading-none tracking-[-0.04em] {(!loading && (stats?.current.running_count ?? 0) > 0) ? 'text-[var(--color-accent-bright)]' : 'text-[var(--color-text-bright)]'}">
						{loading ? '—' : (stats?.current.running_count ?? 0)}
					</div>
				</div>
				<div class="bg-[var(--color-bg-2)] px-6 py-5 transition-colors duration-150 hover:bg-[var(--color-bg-3)]">
					<div class="text-label text-[var(--color-text-muted)]">Peak · 30d</div>
					<div class="mt-1.5 font-serif text-[2.571rem] leading-none tracking-[-0.04em] text-[var(--color-text-bright)]">
						{loading ? '—' : (stats?.peaks.running_count ?? 0)}
					</div>
				</div>
			</div>
		</div>

		<!-- Reserved CPU -->
		<div class="border-r border-[var(--color-border)]" style="box-shadow: inset 3px 0 0 #5a9fd4">
			<div class="flex items-center gap-2 border-b border-[var(--color-border)] bg-[var(--color-bg-3)] px-6 py-3">
				<span class="h-[6px] w-[6px] rounded-full" style="background: #5a9fd4"></span>
				<span class="text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">CPU · vCPUs</span>
			</div>
			<div class="grid grid-cols-2 divide-x divide-[var(--color-border)]">
				<div class="bg-[var(--color-bg-3)] px-6 py-5 transition-colors duration-150 hover:bg-[var(--color-bg-4)]">
					<div class="text-label text-[var(--color-text-muted)]">Reserved now</div>
					<div class="mt-1.5 font-serif text-[2.571rem] leading-none tracking-[-0.04em] text-[var(--color-text-bright)]">
						{loading ? '—' : (stats?.current.vcpus_reserved ?? 0)}
					</div>
				</div>
				<div class="bg-[var(--color-bg-2)] px-6 py-5 transition-colors duration-150 hover:bg-[var(--color-bg-3)]">
					<div class="text-label text-[var(--color-text-muted)]">Peak · 30d</div>
					<div class="mt-1.5 font-serif text-[2.571rem] leading-none tracking-[-0.04em] text-[var(--color-text-bright)]">
						{loading ? '—' : (stats?.peaks.vcpus ?? 0)}
					</div>
				</div>
			</div>
		</div>

		<!-- Reserved RAM -->
		<div style="box-shadow: inset 3px 0 0 #d4a73c">
			<div class="flex items-center gap-2 border-b border-[var(--color-border)] bg-[var(--color-bg-3)] px-6 py-3">
				<span class="h-[6px] w-[6px] rounded-full" style="background: #d4a73c"></span>
				<span class="text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">RAM</span>
			</div>
			<div class="grid grid-cols-2 divide-x divide-[var(--color-border)]">
				<div class="bg-[var(--color-bg-3)] px-6 py-5 transition-colors duration-150 hover:bg-[var(--color-bg-4)]">
					<div class="text-label text-[var(--color-text-muted)]">Reserved now</div>
					<div class="mt-1.5 font-serif text-[2.571rem] leading-none tracking-[-0.04em] text-[var(--color-text-bright)]">
						{loading ? '—' : fmtGB(stats?.current.memory_mb_reserved ?? 0)}
					</div>
				</div>
				<div class="bg-[var(--color-bg-2)] px-6 py-5 transition-colors duration-150 hover:bg-[var(--color-bg-3)]">
					<div class="text-label text-[var(--color-text-muted)]">Peak · 30d</div>
					<div class="mt-1.5 font-serif text-[2.571rem] leading-none tracking-[-0.04em] text-[var(--color-text-bright)]">
						{loading ? '—' : fmtGB(stats?.peaks.memory_mb ?? 0)}
					</div>
				</div>
			</div>
		</div>

	</div>

	<!-- Error state -->
	{#if error}
		<div class="flex items-center gap-3 rounded-[var(--radius-card)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/8 px-4 py-3">
			<svg class="shrink-0 text-[var(--color-red)]" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
				<circle cx="12" cy="12" r="10" /><line x1="12" y1="8" x2="12" y2="12" /><line x1="12" y1="16" x2="12.01" y2="16" />
			</svg>
			<span class="text-ui text-[var(--color-red)]">Failed to load stats: {error}</span>
		</div>
	{/if}

	<!-- Charts -->
	<div class="flex flex-1 flex-col gap-5">

		<!-- Running Capsules -->
		<div class="flex flex-col rounded-[var(--radius-card)] border border-[var(--color-border)] bg-[var(--color-bg-2)]">
			<div class="border-b border-[var(--color-border)] px-6 py-4">
				<div class="flex items-center gap-2">
					<span class="h-[6px] w-[6px] rounded-full bg-[var(--color-accent)]"></span>
					<div class="text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Running Capsules</div>
				</div>
			</div>
			<div class="relative flex-1 px-5 pb-5 pt-3" style="min-height: 220px">
				<canvas bind:this={canvasRunning}></canvas>
			</div>
		</div>

		<!-- Reserved CPU & RAM -->
		<div class="flex flex-col rounded-[var(--radius-card)] border border-[var(--color-border)] bg-[var(--color-bg-2)]">
			<div class="border-b border-[var(--color-border)] px-6 py-4">
				<div class="flex items-center gap-3">
					<span class="flex items-center gap-1.5">
						<span class="h-[6px] w-[6px] rounded-full" style="background: #5a9fd4"></span>
						<span class="text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">CPU</span>
					</span>
					<span class="text-label text-[var(--color-text-muted)]">/</span>
					<span class="flex items-center gap-1.5">
						<span class="h-[6px] w-[6px] rounded-full" style="background: #d4a73c"></span>
						<span class="text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">RAM</span>
					</span>
				</div>
			</div>
			<div class="relative flex-1 px-5 pb-5 pt-3" style="min-height: 220px">
				<canvas bind:this={canvasResource}></canvas>
			</div>
		</div>

	</div>

</div>
