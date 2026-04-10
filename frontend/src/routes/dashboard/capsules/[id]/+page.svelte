<script lang="ts">
	import { onMount, onDestroy, tick } from 'svelte';
	import { page } from '$app/stores';
	import { goto } from '$app/navigation';
	import { getCapsule, type Capsule } from '$lib/api/capsules';
	import FilesTab from '$lib/components/FilesTab.svelte';
	import TerminalTab from '$lib/components/TerminalTab.svelte';
	import {
		fetchSandboxMetrics,
		METRIC_RANGES,
		METRIC_POLL_INTERVAL,
		type MetricRange,
		type MetricPoint
	} from '$lib/api/metrics';

	const sandboxId: string = $page.params.id ?? '';

	let capsule = $state<Capsule | null>(null);
	let capsuleLoading = $state(true);
	let capsuleError = $state<string | null>(null);

	type Tab = 'metrics' | 'files' | 'terminal';
	const VALID_TABS: Tab[] = ['metrics', 'files', 'terminal'];
	let activeTab = $state<Tab>('metrics');

	function setTab(tab: Tab) {
		activeTab = tab;
		const url = new URL(window.location.href);
		if (tab === 'metrics') {
			url.searchParams.delete('tab');
		} else {
			url.searchParams.set('tab', tab);
		}
		history.replaceState(null, '', url.toString());
	}

	let range = $state<MetricRange>('10m');
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

	const metricsAvailable = $derived(
		capsule?.status === 'running' || capsule?.status === 'paused'
	);

	// Latest values for live reading display in chart headers
	const latestCpu = $derived<number | null>(
		points.length > 0 ? points[points.length - 1].cpu_pct : null
	);
	const latestRamMB = $derived<number | null>(
		points.length > 0 ? points[points.length - 1].mem_bytes / 1_048_576 : null
	);

	async function loadCapsule() {
		const result = await getCapsule(sandboxId);
		if (result.ok) {
			capsule = result.data;
			capsuleError = null;
		} else {
			capsuleError = result.error;
		}
		capsuleLoading = false;
	}

	async function loadMetrics() {
		if (!metricsAvailable) return;
		const result = await fetchSandboxMetrics(sandboxId, range);
		if (result.ok) {
			points = result.data.points;
			metricsError = null;
		} else {
			metricsError = result.error;
		}
		metricsLoading = false;
		updateCharts();
	}

	/** Simple moving average — smooths noisy high-frequency samples. */
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

	/** Window size scales with point count — more data = more smoothing. */
	function smoothWindow(count: number): number {
		if (count < 60) return 1;   // < 60 pts: no smoothing
		if (count < 200) return 3;
		if (count < 600) return 5;
		return 9;
	}

	function updateCharts() {
		if (!points.length) return;
		const labels = formatLabels(Array.from(points), range);
		const w = smoothWindow(points.length);
		if (chartCpu) {
			chartCpu.data.labels = labels;
			chartCpu.data.datasets[0].data = smooth(
				Array.from(points.map((p) => +p.cpu_pct.toFixed(2))), w
			);
			chartCpu.update();
		}
		if (chartRam) {
			chartRam.data.labels = labels;
			chartRam.data.datasets[0].data = smooth(
				Array.from(points.map((p) => +(p.mem_bytes / 1_048_576).toFixed(1))), w
			);
			chartRam.update();
		}
	}

	function formatLabels(pts: MetricPoint[], r: MetricRange): string[] {
		return pts.map((p) => {
			const d = new Date(p.timestamp_unix * 1000);
			if (r === '5m' || r === '10m') {
				return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
			}
			return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
		});
	}

	function setRange(r: MetricRange) {
		range = r;
		goto(`?range=${r}`, { replaceState: true, noScroll: true, keepFocus: true });
		metricsLoading = true;
		restartPolling();
	}

	function restartPolling() {
		if (pollInterval) clearInterval(pollInterval);
		loadMetrics();
		pollInterval = setInterval(loadMetrics, METRIC_POLL_INTERVAL);
	}

	// Chart design tokens (match StatsPanel.svelte)
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
				ticks: {
					color: C_TICK,
					font: { family: FONT_MONO, size: 10 },
					maxTicksLimit: 8,
					maxRotation: 0,
				},
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
				datasets: [
					{
						data: [],
						borderColor: C_BLUE,
						backgroundColor: C_BLUE_FILL,
						borderWidth: 2,
						fill: true,
						tension: 0,
						pointRadius: 0,
						pointHoverRadius: 4,
						pointHoverBackgroundColor: C_BLUE,
					},
				],
			},
			options: {
				...BASE_CHART_OPTIONS,
				plugins: {
					...BASE_CHART_OPTIONS.plugins,
					tooltip: {
						...BASE_CHART_OPTIONS.plugins.tooltip,
						callbacks: {
							// eslint-disable-next-line @typescript-eslint/no-explicit-any
							label: (ctx: any) => ` ${ctx.parsed.y.toFixed(1)}%`,
						},
					},
				},
				scales: {
					...BASE_CHART_OPTIONS.scales,
					y: {
						...BASE_CHART_OPTIONS.scales.y,
						ticks: {
							...BASE_CHART_OPTIONS.scales.y.ticks,
							callback: (v: string | number) => `${+v}%`,
						},
					},
				},
			},
		});

		chartRam = new ChartJS(canvasRam, {
			type: 'line',
			data: {
				labels: [],
				datasets: [
					{
						data: [],
						borderColor: C_AMBER,
						backgroundColor: C_AMBER_FILL,
						borderWidth: 2,
						fill: true,
						tension: 0,
						pointRadius: 0,
						pointHoverRadius: 4,
						pointHoverBackgroundColor: C_AMBER,
					},
				],
			},
			options: {
				...BASE_CHART_OPTIONS,
				plugins: {
					...BASE_CHART_OPTIONS.plugins,
					tooltip: {
						...BASE_CHART_OPTIONS.plugins.tooltip,
						callbacks: {
							// eslint-disable-next-line @typescript-eslint/no-explicit-any
							label: (ctx: any) => ` ${ctx.parsed.y.toFixed(0)} MB`,
						},
					},
				},
				scales: {
					...BASE_CHART_OPTIONS.scales,
					y: {
						...BASE_CHART_OPTIONS.scales.y,
						ticks: {
							...BASE_CHART_OPTIONS.scales.y.ticks,
							callback: (v: string | number) => `${+v} MB`,
						},
					},
				},
			},
		});

		updateCharts();
	}

	// Re-create charts whenever the metrics tab becomes active (canvases remount)
	$effect(() => {
		// Only track these two values for re-triggering
		const tab = activeTab;
		const chartLib = ChartJS;

		if (tab !== 'metrics' || !chartLib) return;

		// Wait for canvases to mount after the tab switch
		tick().then(() => {
			if (canvasCpu && canvasRam) {
				initCharts();
				restartPolling();
			}
		});

		return () => {
			if (pollInterval) { clearInterval(pollInterval); pollInterval = null; }
			chartCpu?.destroy(); chartCpu = null;
			chartRam?.destroy(); chartRam = null;
		};
	});

	onMount(async () => {
		const params = new URLSearchParams(window.location.search);

		const urlTab = params.get('tab') as Tab | null;
		if (urlTab && VALID_TABS.includes(urlTab)) {
			activeTab = urlTab;
		}

		const urlRange = params.get('range');
		if (urlRange && METRIC_RANGES.includes(urlRange as MetricRange)) {
			range = urlRange as MetricRange;
		}

		await loadCapsule();

		if (!metricsAvailable) return;

		const mod = await import('chart.js/auto');
		ChartJS = mod.Chart;
	});

	onDestroy(() => {
		if (pollInterval) clearInterval(pollInterval);
		chartCpu?.destroy();
		chartRam?.destroy();
	});

	function statusColor(status: string): string {
		switch (status) {
			case 'running': return 'var(--color-accent)';
			case 'paused':  return 'var(--color-amber)';
			case 'error':   return 'var(--color-red)';
			default:        return 'var(--color-text-muted)';
		}
	}

	function statusBg(status: string): string {
		switch (status) {
			case 'running': return 'rgba(94,140,88,0.12)';
			case 'paused':  return 'rgba(212,167,60,0.12)';
			case 'error':   return 'rgba(207,129,114,0.12)';
			default:        return 'rgba(255,255,255,0.05)';
		}
	}

	function statusBorder(status: string): string {
		switch (status) {
			case 'running': return 'rgba(94,140,88,0.3)';
			case 'paused':  return 'rgba(212,167,60,0.3)';
			case 'error':   return 'rgba(207,129,114,0.3)';
			default:        return 'rgba(255,255,255,0.08)';
		}
	}

	function fmtDate(iso: string | null | undefined): string {
		if (!iso) return '—';
		return new Date(iso).toLocaleString([], {
			month: 'short', day: 'numeric',
			hour: '2-digit', minute: '2-digit',
		});
	}

	function fmtTimeout(sec: number): string {
		if (!sec) return 'None';
		if (sec < 60) return `${sec}s`;
		if (sec < 3600) return `${Math.round(sec / 60)}m`;
		return `${Math.round(sec / 3600)}h`;
	}
</script>

<svelte:head>
	<title>Wrenn — {sandboxId}</title>
</svelte:head>

<style>
	.metric-val {
		transition: color 0.3s ease;
	}
	@keyframes fadeSlideUp {
		from { opacity: 0; transform: translateY(6px); }
		to   { opacity: 1; transform: translateY(0); }
	}
	.anim-in {
		animation: fadeSlideUp 0.28s ease both;
	}
</style>

{#if capsuleLoading}
	<div class="flex items-center justify-center py-24">
		<div class="flex items-center gap-3 text-ui text-[var(--color-text-secondary)]">
			<svg class="animate-spin" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
				<path d="M21 12a9 9 0 1 1-6.219-8.56" />
			</svg>
			Loading capsule...
		</div>
	</div>
{:else if capsuleError}
	<div class="px-7 py-8">
		<div class="flex items-center gap-3 rounded-[var(--radius-card)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/8 px-5 py-4">
			<svg class="shrink-0 text-[var(--color-red)]" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
				<circle cx="12" cy="12" r="10" /><line x1="12" y1="8" x2="12" y2="12" /><line x1="12" y1="16" x2="12.01" y2="16" />
			</svg>
			<span class="text-ui text-[var(--color-red)]">{capsuleError}</span>
		</div>
	</div>
{:else if capsule}
<div class="flex flex-1 flex-col min-h-0">

	<!-- Tabs (matches Templates page pattern) -->
	<div class="mt-5 flex gap-0 border-b border-[var(--color-border)] px-7">
			<button
				onclick={() => setTab('metrics')}
				class="flex items-center gap-2 border-b-2 px-4 py-2.5 text-ui font-medium transition-colors duration-150
					{activeTab === 'metrics'
						? 'border-[var(--color-accent)] text-[var(--color-accent-bright)]'
						: 'border-transparent text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]'}"
			>
				<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
					<polyline points="22 12 18 12 15 21 9 3 6 12 2 12" />
				</svg>
				Stats
			</button>

			<button
				onclick={() => setTab('files')}
				class="flex items-center gap-2 border-b-2 px-4 py-2.5 text-ui font-medium transition-colors duration-150
					{activeTab === 'files'
						? 'border-[var(--color-accent)] text-[var(--color-accent-bright)]'
						: 'border-transparent text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]'}"
			>
				<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
					<path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z" />
				</svg>
				Files
			</button>

			<button
				onclick={() => setTab('terminal')}
				class="flex items-center gap-2 border-b-2 px-4 py-2.5 text-ui font-medium transition-colors duration-150
					{activeTab === 'terminal'
						? 'border-[var(--color-accent)] text-[var(--color-accent-bright)]'
						: 'border-transparent text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]'}"
			>
				<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
					<polyline points="4 17 10 11 4 5" /><line x1="12" y1="19" x2="20" y2="19" />
				</svg>
				Terminal
			</button>
	</div>

	<!-- Tab content -->
	<!-- Terminal stays mounted so sessions survive tab switches -->
	<div class="flex flex-1 min-h-0" style:display={activeTab === 'terminal' ? 'flex' : 'none'}>
		<TerminalTab sandboxId={sandboxId} isRunning={capsule.status === 'running'} visible={activeTab === 'terminal'} />
	</div>
	{#if activeTab === 'files'}
		<div class="anim-in flex flex-1 min-h-0" style="animation-delay: 0.05s">
			<FilesTab sandboxId={sandboxId} isRunning={capsule.status === 'running'} />
		</div>
	{:else if activeTab === 'metrics'}
		<div
			class="anim-in flex flex-1 flex-col gap-5 min-h-0 p-8"
			style="animation-delay: 0.05s"
		>

			<!-- Controls row -->
			<div class="flex items-center justify-between">
				{#if metricsAvailable && !metricsLoading}
					<span class="flex items-center gap-1.5 rounded-[3px] border border-[var(--color-accent)]/25 bg-[var(--color-accent-glow-mid)] px-2 py-1 text-badge font-semibold uppercase tracking-[0.05em] text-[var(--color-accent-mid)]">
						<span class="h-[5px] w-[5px] rounded-full bg-[var(--color-accent)]" style="animation: wrenn-glow 2.5s ease-in-out infinite"></span>
						Live
					</span>
				{:else}
					<div></div>
				{/if}

				{#if metricsAvailable}
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
				{/if}
			</div>

			<!-- Info card (StatsPanel style) -->
			<div class="overflow-hidden rounded-[var(--radius-card)] border border-[var(--color-border)]">
				<div class="flex divide-x divide-[var(--color-border)]">

					<!-- Status -->
					<div class="flex flex-1 flex-col gap-2.5 bg-[var(--color-bg-3)] px-6 py-5" style="box-shadow: inset 5px 0 0 {statusColor(capsule.status)}">
						<div class="text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Status</div>
						<span
							class="inline-flex items-center gap-1.5 self-start rounded-full px-2.5 py-1 text-label font-semibold uppercase tracking-[0.05em]"
							style="color: {statusColor(capsule.status)}; background: {statusBg(capsule.status)}; border: 1px solid {statusBorder(capsule.status)}"
						>
							{#if capsule.status === 'running'}
								<span class="relative flex h-[5px] w-[5px] shrink-0">
									<span class="animate-status-ping absolute inline-flex h-full w-full rounded-full bg-[var(--color-accent)]"></span>
									<span class="relative inline-flex h-[5px] w-[5px] rounded-full bg-[var(--color-accent)]"></span>
								</span>
							{/if}
							{capsule.status}
						</span>
					</div>

					<!-- Template -->
					<div class="flex flex-1 flex-col gap-2.5 bg-[var(--color-bg-3)] px-6 py-5">
						<div class="text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Template</div>
						<span class="font-mono text-ui text-[var(--color-text-bright)]">{capsule.template}</span>
					</div>

					<!-- CPU -->
					<div class="flex flex-1 flex-col gap-2.5 bg-[var(--color-bg-3)] px-6 py-5">
						<div class="text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]">CPU</div>
						<div class="mt-0.5 flex items-baseline gap-1">
							<span class="font-serif text-[2.571rem] leading-none tracking-[-0.04em] text-[var(--color-text-bright)]">{capsule.vcpus}</span>
							<span class="font-mono text-label text-[var(--color-text-muted)]">vCPU{capsule.vcpus !== 1 ? 's' : ''}</span>
						</div>
					</div>

					<!-- Memory -->
					<div class="flex flex-1 flex-col gap-2.5 bg-[var(--color-bg-3)] px-6 py-5">
						<div class="text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]">Memory</div>
						<div class="mt-0.5 flex items-baseline gap-1">
							<span class="font-serif text-[2.571rem] leading-none tracking-[-0.04em] text-[var(--color-text-bright)]">{capsule.memory_mb}</span>
							<span class="font-mono text-label text-[var(--color-text-muted)]">MB</span>
						</div>
					</div>

					<!-- Disk -->
					<div class="flex flex-1 flex-col gap-2.5 bg-[var(--color-bg-3)] px-6 py-5">
						<div class="text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]">Disk</div>
						<span class="mt-0.5 font-serif text-[2.571rem] leading-none tracking-[-0.04em] text-[var(--color-text-muted)]">—</span>
					</div>

					<!-- Started -->
					<div class="flex flex-1 flex-col gap-2.5 bg-[var(--color-bg-3)] px-6 py-5">
						<div class="text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Started</div>
						<span class="font-mono text-ui text-[var(--color-text-secondary)]">{fmtDate(capsule.started_at)}</span>
					</div>

					<!-- Idle Timeout -->
					<div class="flex flex-1 flex-col gap-2.5 bg-[var(--color-bg-3)] px-6 py-5">
						<div class="text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Idle Timeout</div>
						<span class="font-mono text-ui text-[var(--color-text-secondary)]">{fmtTimeout(capsule.timeout_sec)}</span>
					</div>

				</div>
			</div>

			{#if metricsError}
				<div class="flex items-center gap-3 rounded-[var(--radius-card)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/8 px-4 py-3">
					<svg class="shrink-0 text-[var(--color-red)]" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
						<circle cx="12" cy="12" r="10" /><line x1="12" y1="8" x2="12" y2="12" /><line x1="12" y1="16" x2="12.01" y2="16" />
					</svg>
					<span class="text-ui text-[var(--color-red)]">Could not load metrics: {metricsError}. Will retry automatically.</span>
				</div>
			{/if}

			{#if metricsAvailable}
				<!-- Charts stacked — grow to fill remaining space -->
				<div class="flex flex-1 flex-col gap-5 min-h-0">

					<!-- CPU Usage -->
					<div class="flex flex-1 flex-col rounded-[var(--radius-card)] border border-[var(--color-border)] bg-[var(--color-bg-2)]">
						<div class="flex items-center justify-between border-b border-[var(--color-border)] px-6 py-4">
							<div class="flex items-center gap-2">
								<span class="h-2 w-2 shrink-0 rounded-full" style="background: #5a9fd4"></span>
								<span class="text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">CPU Usage</span>
							</div>
							{#if latestCpu !== null}
								<div class="flex items-baseline gap-1">
									<span class="metric-val font-serif text-[2.571rem] leading-none tracking-[-0.04em] text-[var(--color-text-bright)]">{latestCpu.toFixed(1)}</span>
									<span class="font-mono text-label text-[var(--color-text-muted)]">%</span>
								</div>
							{:else if metricsLoading}
								<span class="font-serif text-[2.571rem] leading-none text-[var(--color-text-muted)]">—</span>
							{/if}
						</div>
						<div class="relative flex-1 min-h-[180px] px-5 pb-5 pt-3">
							<canvas bind:this={canvasCpu}></canvas>
						</div>
					</div>

					<!-- RAM Usage -->
					<div class="flex flex-1 flex-col rounded-[var(--radius-card)] border border-[var(--color-border)] bg-[var(--color-bg-2)]">
						<div class="flex items-center justify-between border-b border-[var(--color-border)] px-6 py-4">
							<div class="flex items-center gap-2">
								<span class="h-2 w-2 shrink-0 rounded-full" style="background: #d4a73c"></span>
								<span class="text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">RAM Usage</span>
							</div>
							{#if latestRamMB !== null}
								<div class="flex items-baseline gap-1">
									<span class="metric-val font-serif text-[2.571rem] leading-none tracking-[-0.04em] text-[var(--color-text-bright)]">{latestRamMB.toFixed(0)}</span>
									<span class="font-mono text-label text-[var(--color-text-muted)]">MB</span>
								</div>
							{:else if metricsLoading}
								<span class="font-serif text-[2.571rem] leading-none text-[var(--color-text-muted)]">—</span>
							{/if}
						</div>
						<div class="relative flex-1 min-h-[180px] px-5 pb-5 pt-3">
							<canvas bind:this={canvasRam}></canvas>
						</div>
					</div>

				</div>
			{:else}
				<!-- Stats unavailable — capsule not running/paused -->
				<div class="flex items-center gap-3 rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] px-5 py-4">
					<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="var(--color-text-muted)" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
						<polyline points="22 12 18 12 15 21 9 3 6 12 2 12" />
					</svg>
					<span class="text-ui text-[var(--color-text-tertiary)]">
						Live stats are only available for running or paused capsules —
						current status: <span class="font-mono" style="color: {statusColor(capsule.status)}">{capsule.status}</span>
					</span>
				</div>
			{/if}

		</div>
	{/if}
</div>
{/if}
