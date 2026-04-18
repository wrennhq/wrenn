<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { fetchUsage, defaultRange, formatDate, type UsageResponse } from '$lib/api/usage';

	// ─── State ────────────────────────────────────────────────────────────────

	const PRESETS = ['7d', '30d', '90d'] as const;
	type Preset = (typeof PRESETS)[number];

	let preset = $state<Preset | null>('30d');
	let fromInput = $state('');
	let toInput = $state('');
	let data = $state<UsageResponse | null>(null);
	let loading = $state(true);
	let error = $state<string | null>(null);

	let canvasCpu = $state<HTMLCanvasElement | null>(null);
	let canvasRam = $state<HTMLCanvasElement | null>(null);
	// eslint-disable-next-line @typescript-eslint/no-explicit-any
	let chartCpu: any = null;
	// eslint-disable-next-line @typescript-eslint/no-explicit-any
	let chartRam: any = null;
	// eslint-disable-next-line @typescript-eslint/no-explicit-any
	let ChartJS: any = null;

	// ─── Derived ──────────────────────────────────────────────────────────────

	let totalCpuMinutes = $derived(
		data?.points.reduce((sum, p) => sum + p.cpu_minutes, 0) ?? 0
	);
	let totalRamGBMinutes = $derived(
		(data?.points.reduce((sum, p) => sum + p.ram_mb_minutes, 0) ?? 0) / 1024
	);

	// ─── Chart config ─────────────────────────────────────────────────────────

	const C_BLUE       = '#5a9fd4';
	const C_BLUE_FILL  = 'rgba(90,159,212,0.55)';
	const C_AMBER      = '#d4a73c';
	const C_AMBER_FILL = 'rgba(212,167,60,0.55)';
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
				grid: { display: false },
				ticks: { color: C_TICK, font: { family: FONT_MONO, size: 10 }, maxTicksLimit: 12, maxRotation: 0 },
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

	// ─── Data loading ─────────────────────────────────────────────────────────

	async function load() {
		loading = true;
		error = null;
		const result = await fetchUsage(fromInput, toInput);
		if (result.ok) {
			data = result.data;
		} else {
			error = result.error;
		}
		loading = false;
		updateCharts();
	}

	let boundCpuCanvas: HTMLCanvasElement | null = null;
	let boundRamCanvas: HTMLCanvasElement | null = null;

	function initCharts() {
		if (!ChartJS || !canvasCpu || !canvasRam) return;
		// Skip if already bound to these exact canvas elements.
		if (boundCpuCanvas === canvasCpu && boundRamCanvas === canvasRam) {
			updateCharts();
			return;
		}
		// Destroy stale instances if canvases were re-mounted.
		chartCpu?.destroy();
		chartRam?.destroy();
		boundCpuCanvas = canvasCpu;
		boundRamCanvas = canvasRam;

		chartCpu = new ChartJS(canvasCpu, {
			type: 'bar',
			data: {
				labels: [],
				datasets: [{
					data: [],
					backgroundColor: C_BLUE_FILL,
					borderColor: C_BLUE,
					borderWidth: 1,
					borderRadius: 2,
					hoverBackgroundColor: C_BLUE,
				}],
			},
			options: {
				...BASE_CHART_OPTIONS,
				plugins: {
					...BASE_CHART_OPTIONS.plugins,
					tooltip: {
						...BASE_CHART_OPTIONS.plugins.tooltip,
						callbacks: {
							// eslint-disable-next-line @typescript-eslint/no-explicit-any
							label: (ctx: any) => ` ${ctx.parsed.y.toFixed(1)} min`,
						},
					},
				},
				scales: {
					...BASE_CHART_OPTIONS.scales,
					y: {
						...BASE_CHART_OPTIONS.scales.y,
						ticks: {
							...BASE_CHART_OPTIONS.scales.y.ticks,
							callback: (v: string | number) => `${(+v).toFixed(0)}`,
						},
					},
				},
			},
		});

		chartRam = new ChartJS(canvasRam, {
			type: 'bar',
			data: {
				labels: [],
				datasets: [{
					data: [],
					backgroundColor: C_AMBER_FILL,
					borderColor: C_AMBER,
					borderWidth: 1,
					borderRadius: 2,
					hoverBackgroundColor: C_AMBER,
				}],
			},
			options: {
				...BASE_CHART_OPTIONS,
				plugins: {
					...BASE_CHART_OPTIONS.plugins,
					tooltip: {
						...BASE_CHART_OPTIONS.plugins.tooltip,
						callbacks: {
							// eslint-disable-next-line @typescript-eslint/no-explicit-any
							label: (ctx: any) => ` ${ctx.parsed.y.toFixed(2)} GB-min`,
						},
					},
				},
				scales: {
					...BASE_CHART_OPTIONS.scales,
					y: {
						...BASE_CHART_OPTIONS.scales.y,
						ticks: {
							...BASE_CHART_OPTIONS.scales.y.ticks,
							callback: (v: string | number) => `${(+v).toFixed(1)}`,
						},
					},
				},
			},
		});

		updateCharts();
	}

	function updateCharts() {
		if (!data) return;

		// Build a lookup from date string → point for O(1) access.
		const pointMap = new Map(data.points.map((p) => [p.date, p]));

		// Generate a label + value for every day in the range so bars are
		// evenly distributed and days with no usage show as zero.
		const labels: string[] = [];
		const cpuData: number[] = [];
		const ramData: number[] = [];

		// Use UTC dates to avoid timezone-induced date shifts when
		// comparing against the YYYY-MM-DD keys from the API.
		const from = new Date(fromInput + 'T00:00:00Z');
		const to = new Date(toInput + 'T00:00:00Z');
		for (const d = new Date(from); d <= to; d.setUTCDate(d.getUTCDate() + 1)) {
			const key = d.toISOString().slice(0, 10);
			const pt = pointMap.get(key);
			labels.push(new Date(key + 'T00:00:00').toLocaleDateString([], { month: 'short', day: 'numeric' }));
			cpuData.push(pt ? +pt.cpu_minutes.toFixed(2) : 0);
			ramData.push(pt ? +(pt.ram_mb_minutes / 1024).toFixed(2) : 0);
		}

		if (chartCpu) {
			chartCpu.data.labels = labels;
			chartCpu.data.datasets[0].data = cpuData;
			chartCpu.update();
		}
		if (chartRam) {
			chartRam.data.labels = labels;
			chartRam.data.datasets[0].data = ramData;
			chartRam.update();
		}
	}

	// ─── Range controls ───────────────────────────────────────────────────────

	function applyPreset(p: Preset) {
		preset = p;
		const to = new Date();
		const from = new Date(to);
		const days = p === '7d' ? 6 : p === '30d' ? 29 : 89;
		from.setDate(from.getDate() - days);
		fromInput = formatDate(from);
		toInput = formatDate(to);
		load();
	}

	function onDateChange() {
		// Clear preset highlight when custom dates are used
		const to = new Date();
		const from7 = new Date(to); from7.setDate(from7.getDate() - 6);
		const from30 = new Date(to); from30.setDate(from30.getDate() - 29);
		const from90 = new Date(to); from90.setDate(from90.getDate() - 89);
		const todayStr = formatDate(to);

		if (toInput === todayStr) {
			if (fromInput === formatDate(from7)) preset = '7d';
			else if (fromInput === formatDate(from30)) preset = '30d';
			else if (fromInput === formatDate(from90)) preset = '90d';
			else preset = null;
		}
		load();
	}

	// ─── Formatting ───────────────────────────────────────────────────────────

	function fmtNumber(n: number): string {
		if (n >= 1000) return n.toLocaleString('en-US', { maximumFractionDigits: 1 });
		return n.toFixed(1);
	}

	// ─── Lifecycle ────────────────────────────────────────────────────────────

	// When canvas elements appear in the DOM (after data loads), init charts.
	$effect(() => {
		if (canvasCpu && canvasRam && ChartJS) {
			initCharts();
		}
	});

	onMount(async () => {
		const { from, to } = defaultRange();
		fromInput = from;
		toInput = to;

		const mod = await import('chart.js/auto');
		ChartJS = mod.Chart;

		await load();
	});

	onDestroy(() => {
		chartCpu?.destroy();
		chartRam?.destroy();
	});
</script>

<svelte:head>
	<title>Wrenn — Usage</title>
</svelte:head>

<main class="flex-1 overflow-y-auto bg-[var(--color-bg-0)]">

	<!-- Header -->
	<div class="px-7 pt-8">
		<h1 class="font-serif text-page text-[var(--color-text-bright)]">
			Usage
		</h1>
		<p class="mt-2 text-ui text-[var(--color-text-secondary)]">
			CPU and memory consumed by your capsules, aggregated daily.
		</p>
		<div class="mt-6 border-b border-[var(--color-border)]"></div>
	</div>

	<!-- Content -->
	<div class="p-8" style="animation: fadeUp 0.35s ease both">

		<!-- Controls row -->
		<div class="flex items-center justify-between">
			<!-- Preset range selector -->
			<div class="flex items-center gap-3">
				<span class="text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Period</span>
				<div class="flex overflow-hidden rounded-[var(--radius-button)] border border-[var(--color-border)]">
					{#each PRESETS as p, i}
						<button
							onclick={() => applyPreset(p)}
							class="px-3 py-1.5 font-mono text-label transition-colors duration-150
								{preset === p
									? 'bg-[var(--color-bg-5)] text-[var(--color-text-bright)]'
									: 'text-[var(--color-text-tertiary)] hover:bg-[var(--color-bg-3)] hover:text-[var(--color-text-secondary)]'}
								{i > 0 ? 'border-l border-[var(--color-border)]' : ''}"
						>
							{p}
						</button>
					{/each}
				</div>
			</div>

			<!-- Date inputs -->
			<div class="flex items-center gap-2.5">
				<input
					type="date"
					bind:value={fromInput}
					onchange={onDateChange}
					class="rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-2)] px-2.5 py-1.5 font-mono text-label text-[var(--color-text-secondary)] transition-colors duration-150 focus:border-[var(--color-accent)] focus:outline-none"
				/>
				<span class="text-meta text-[var(--color-text-tertiary)]">to</span>
				<input
					type="date"
					bind:value={toInput}
					onchange={onDateChange}
					class="rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-2)] px-2.5 py-1.5 font-mono text-label text-[var(--color-text-secondary)] transition-colors duration-150 focus:border-[var(--color-accent)] focus:outline-none"
				/>
			</div>
		</div>

		<!-- Error state -->
		{#if error}
			<div class="mt-6 flex items-center justify-between gap-4 rounded-[var(--radius-card)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-4 py-3 text-ui text-[var(--color-red)]">
				<span>{error}</span>
				<button
					onclick={load}
					class="shrink-0 font-semibold underline-offset-2 hover:underline"
				>
					Try again
				</button>
			</div>
		{/if}

		{#if loading}
			<div class="flex items-center justify-center py-24">
				<div class="flex items-center gap-3 text-ui text-[var(--color-text-secondary)]">
					<svg class="animate-spin" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
						<path d="M21 12a9 9 0 1 1-6.219-8.56" />
					</svg>
					Loading usage data…
				</div>
			</div>
		{:else if data && data.points.length === 0}
			<!-- Empty state -->
			<div class="flex flex-col items-center justify-center py-[72px]">
				<div class="relative mb-5">
					<div class="absolute inset-0 -m-6 rounded-full" style="background: radial-gradient(circle, rgba(90,159,212,0.06) 0%, transparent 70%)"></div>
					<div class="relative flex h-14 w-14 items-center justify-center rounded-[var(--radius-card)] border border-[var(--color-blue)]/20 bg-[var(--color-bg-3)]" style="animation: iconFloat 4s ease-in-out infinite">
						<svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="var(--color-blue)" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
							<rect x="3" y="12" width="4" height="9" rx="1" />
							<rect x="10" y="7" width="4" height="14" rx="1" />
							<rect x="17" y="3" width="4" height="18" rx="1" />
						</svg>
					</div>
				</div>
				<p class="font-serif text-heading text-[var(--color-text-bright)]">
					No usage recorded
				</p>
				<p class="mt-1.5 text-ui text-[var(--color-text-tertiary)]">
					Metrics appear here once you run a capsule. Create one to get started.
				</p>
			</div>
		{:else if data}
			<!-- Summary cards -->
			<div class="mt-6 grid grid-cols-2 gap-4">

				<!-- CPU total -->
				<div class="overflow-hidden rounded-[var(--radius-card)] border border-[var(--color-border)]" style="box-shadow: inset 3px 0 0 #5a9fd4; animation: fadeUp 0.35s ease both; animation-delay: 40ms">
					<div class="flex items-center gap-2 border-b border-[var(--color-border)] bg-[var(--color-bg-3)] px-5 py-2.5">
						<span class="h-1.5 w-1.5 rounded-full" style="background: #5a9fd4"></span>
						<span class="text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">CPU time</span>
					</div>
					<div class="bg-[var(--color-bg-2)] px-5 py-5 transition-colors duration-150 hover:bg-[var(--color-bg-3)]">
						<div class="font-serif text-display leading-none tracking-[-0.04em] text-[var(--color-text-bright)]">
							{fmtNumber(totalCpuMinutes)}
						</div>
						<div class="mt-1.5 font-mono text-meta text-[var(--color-text-tertiary)]">
							minutes
						</div>
					</div>
				</div>

				<!-- RAM total -->
				<div class="overflow-hidden rounded-[var(--radius-card)] border border-[var(--color-border)]" style="box-shadow: inset 3px 0 0 #d4a73c; animation: fadeUp 0.35s ease both; animation-delay: 80ms">
					<div class="flex items-center gap-2 border-b border-[var(--color-border)] bg-[var(--color-bg-3)] px-5 py-2.5">
						<span class="h-1.5 w-1.5 rounded-full" style="background: #d4a73c"></span>
						<span class="text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Memory</span>
					</div>
					<div class="bg-[var(--color-bg-2)] px-5 py-5 transition-colors duration-150 hover:bg-[var(--color-bg-3)]">
						<div class="font-serif text-display leading-none tracking-[-0.04em] text-[var(--color-text-bright)]">
							{fmtNumber(totalRamGBMinutes)}
						</div>
						<div class="mt-1.5 font-mono text-meta text-[var(--color-text-tertiary)]">
							GB-minutes
						</div>
					</div>
				</div>

			</div>

			<!-- Charts -->
			<div class="mt-6 flex flex-col gap-6">

				<!-- CPU chart -->
				<div class="flex flex-col rounded-[var(--radius-card)] border border-[var(--color-border)] bg-[var(--color-bg-2)]" style="animation: fadeUp 0.35s ease both; animation-delay: 120ms">
					<div class="border-b border-[var(--color-border)] px-5 py-3">
						<div class="flex items-center gap-2">
							<span class="h-1.5 w-1.5 rounded-full" style="background: #5a9fd4"></span>
							<span class="text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">CPU minutes per day</span>
						</div>
					</div>
					<div class="relative flex-1 px-5 pb-5 pt-3" style="min-height: 260px">
						<canvas bind:this={canvasCpu}></canvas>
					</div>
				</div>

				<!-- RAM chart -->
				<div class="flex flex-col rounded-[var(--radius-card)] border border-[var(--color-border)] bg-[var(--color-bg-2)]" style="animation: fadeUp 0.35s ease both; animation-delay: 160ms">
					<div class="border-b border-[var(--color-border)] px-5 py-3">
						<div class="flex items-center gap-2">
							<span class="h-1.5 w-1.5 rounded-full" style="background: #d4a73c"></span>
							<span class="text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Memory GB-minutes per day</span>
						</div>
					</div>
					<div class="relative flex-1 px-5 pb-5 pt-3" style="min-height: 260px">
						<canvas bind:this={canvasRam}></canvas>
					</div>
				</div>

			</div>
		{/if}
	</div>
</main>

<footer class="flex h-7 shrink-0 items-center justify-end border-t border-[var(--color-border)] bg-[var(--color-bg-1)] px-7">
	<div class="flex items-center gap-1.5">
		<span class="relative flex h-[5px] w-[5px]">
			<span class="animate-status-ping absolute inline-flex h-full w-full rounded-full bg-[var(--color-accent)]"></span>
			<span class="relative inline-flex h-[5px] w-[5px] rounded-full bg-[var(--color-accent)]"></span>
		</span>
		<span class="font-mono text-label uppercase tracking-[0.04em] text-[var(--color-text-secondary)]">All systems operational</span>
	</div>
</footer>

<style>
	/* Dark theme date input overrides */
	input[type='date']::-webkit-calendar-picker-indicator {
		filter: invert(0.6);
		cursor: pointer;
	}
	input[type='date']::-webkit-datetime-edit {
		color: var(--color-text-secondary);
	}
</style>
