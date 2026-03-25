<script lang="ts">
	import Sidebar from '$lib/components/Sidebar.svelte';
	import { onMount } from 'svelte';
	import { toast } from '$lib/toast.svelte';
	import { auth } from '$lib/auth.svelte';
	import {
		listCapsules,
		createCapsule,
		pauseCapsule,
		resumeCapsule,
		destroyCapsule,
		createSnapshot,
		type Capsule,
		type CreateCapsuleParams
	} from '$lib/api/capsules';

	const REFRESH_INTERVAL = 30;
	const SPIN_DURATION = 600; // ms — minimum full rotation time

	let collapsed = $state(
		typeof window !== 'undefined'
			? localStorage.getItem('wrenn_sidebar_collapsed') === 'true'
			: false
	);
	let activeTab: 'list' | 'stats' = $state('list');

	// Capsule list state
	let capsules = $state<Capsule[]>([]);
	let loading = $state(true);
	let error = $state<string | null>(null);
	let searchQuery = $state('');
	let actionLoading = $state<string | null>(null);
	let spinning = $state(false);

	// Auto-refresh countdown state
	let autoRefresh = $state(true);
	let countdown = $state(REFRESH_INTERVAL);
	let countdownInterval: ReturnType<typeof setInterval> | null = null;
	let refreshInterval: ReturnType<typeof setInterval> | null = null;

	// Sorting state
	type SortKey = 'status' | 'vcpus' | 'memory_mb' | 'started_at' | 'timeout_sec';
	let sortKey = $state<SortKey | null>(null);
	let sortDir = $state<'asc' | 'desc'>('asc');

	// Status menu state
	let openMenuId = $state<string | null>(null);
	let menuPos = $state<{ top: number; left: number }>({ top: 0, left: 0 });

	// Create dialog state
	let showCreateDialog = $state(false);
	let createForm = $state<CreateCapsuleParams>({ template: 'minimal', vcpus: 1, memory_mb: 512, timeout_sec: 0 });
	let creating = $state(false);
	let createError = $state<string | null>(null);

	// Snapshot dialog state
	let snapshotTarget = $state<{ capsule: Capsule; pauseFirst: boolean } | null>(null);
	let snapshotName = $state('');
	let snapshotting = $state(false);
	let snapshotError = $state<string | null>(null);

	// Destroy confirmation state
	let destroyTarget = $state<Capsule | null>(null);
	let destroying = $state(false);
	let destroyError = $state<string | null>(null);

	// Delight: briefly highlight a newly created capsule row
	let newCapsuleId = $state<string | null>(null);

	let filteredCapsules = $derived.by(() => {
		let list = searchQuery
			? capsules.filter((c) => c.id.toLowerCase().includes(searchQuery.toLowerCase()))
			: [...capsules];

		if (sortKey) {
			const key = sortKey;
			const dir = sortDir === 'asc' ? 1 : -1;
			list.sort((a, b) => {
				if (key === 'status') {
					return a.status.localeCompare(b.status) * dir;
				}
				if (key === 'started_at') {
					const ta = a.started_at ? new Date(a.started_at).getTime() : 0;
					const tb = b.started_at ? new Date(b.started_at).getTime() : 0;
					return (ta - tb) * dir;
				}
				const va = a[key] as number;
				const vb = b[key] as number;
				return (va - vb) * dir;
			});
		}

		return list;
	});

	let runningCount = $derived(capsules.filter((c) => c.status === 'running').length);

	function toggleSort(key: SortKey) {
		if (sortKey === key) {
			sortDir = sortDir === 'asc' ? 'desc' : 'asc';
		} else {
			sortKey = key;
			sortDir = 'asc';
		}
	}

	function startAutoRefresh() {
		stopAutoRefresh();
		countdown = REFRESH_INTERVAL;
		countdownInterval = setInterval(() => {
			countdown--;
			if (countdown <= 0) {
				countdown = REFRESH_INTERVAL;
			}
		}, 1000);
		refreshInterval = setInterval(fetchCapsules, REFRESH_INTERVAL * 1000);
	}

	function stopAutoRefresh() {
		if (countdownInterval) { clearInterval(countdownInterval); countdownInterval = null; }
		if (refreshInterval) { clearInterval(refreshInterval); refreshInterval = null; }
	}

	function toggleAutoRefresh() {
		autoRefresh = !autoRefresh;
		if (autoRefresh) {
			startAutoRefresh();
		} else {
			stopAutoRefresh();
		}
	}

	async function fetchCapsules() {
		const wasEmpty = capsules.length === 0;
		if (wasEmpty) loading = true;

		// Spin for at least SPIN_DURATION ms
		spinning = true;
		const spinTimer = new Promise<void>((resolve) => setTimeout(resolve, SPIN_DURATION));

		error = null;
		const result = await listCapsules();
		if (result.ok) {
			capsules = result.data;
		} else {
			error = result.error;
		}
		loading = false;

		// Reset countdown on manual or auto refresh
		if (autoRefresh) countdown = REFRESH_INTERVAL;

		await spinTimer;
		spinning = false;
	}

	async function handlePause(id: string) {
		openMenuId = null;
		actionLoading = id;
		const result = await pauseCapsule(id);
		if (result.ok) {
			capsules = capsules.map((c) => (c.id === id ? result.data : c));
		} else {
			toast.error(result.error);
		}
		actionLoading = null;
	}

	async function handleResume(id: string) {
		openMenuId = null;
		actionLoading = id;
		const result = await resumeCapsule(id);
		if (result.ok) {
			capsules = capsules.map((c) => (c.id === id ? result.data : c));
		} else {
			toast.error(result.error);
		}
		actionLoading = null;
	}

	function handleSnapshot(capsule: Capsule) {
		openMenuId = null;
		snapshotName = '';
		snapshotError = null;
		snapshotTarget = { capsule, pauseFirst: false };
	}

	function handlePauseAndSnapshot(capsule: Capsule) {
		openMenuId = null;
		snapshotName = '';
		snapshotError = null;
		snapshotTarget = { capsule, pauseFirst: true };
	}

	async function handleSnapshotConfirm() {
		if (!snapshotTarget) return;
		snapshotting = true;
		snapshotError = null;
		const result = await createSnapshot(snapshotTarget.capsule.id, snapshotName.trim() || undefined);
		if (result.ok) {
			snapshotTarget = null;
			await fetchCapsules();
		} else {
			snapshotError = result.error;
		}
		snapshotting = false;
	}

	async function handleDestroy() {
		if (!destroyTarget) return;
		destroying = true;
		destroyError = null;
		const id = destroyTarget.id;
		const result = await destroyCapsule(id);
		if (result.ok) {
			capsules = capsules.filter((c) => c.id !== id);
			destroyTarget = null;
		} else {
			destroyError = result.error;
		}
		destroying = false;
	}

	async function handleCreate() {
		creating = true;
		createError = null;
		const result = await createCapsule(createForm);
		if (result.ok) {
			capsules = [result.data, ...capsules];
			showCreateDialog = false;
			createForm = { template: 'minimal', vcpus: 1, memory_mb: 512, timeout_sec: 0 };
			// Flash the new row briefly
			newCapsuleId = result.data.id;
			setTimeout(() => { newCapsuleId = null; }, 1600);
		} else {
			createError = result.error;
		}
		creating = false;
	}

	function formatTime(iso: string | undefined): string {
		if (!iso) return '—';
		const d = new Date(iso);
		return d.toLocaleString('en-US', {
			month: 'short',
			day: 'numeric',
			hour: '2-digit',
			minute: '2-digit',
			second: '2-digit',
			hour12: false
		});
	}

	function timeAgo(iso: string | undefined): string {
		if (!iso) return '';
		const seconds = Math.floor((Date.now() - new Date(iso).getTime()) / 1000);
		if (seconds < 60) return `${seconds}s ago`;
		if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
		if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
		return `${Math.floor(seconds / 86400)}d ago`;
	}

	function handleClickOutside(event: MouseEvent) {
		if (openMenuId && !(event.target as Element)?.closest('.status-menu-container')) {
			openMenuId = null;
		}
	}

	// Initial fetch + auto-refresh setup
	onMount(() => {
		fetchCapsules();
		startAutoRefresh();
		return () => stopAutoRefresh();
	});
</script>

<style>
	.refresh-spin {
		animation: spin-once 0.6s ease-in-out;
	}

	/* Row born flash — new capsule appears with a brief accent glow */
	@keyframes capsule-born {
		0%, 25% { background-color: rgba(94, 140, 88, 0.1); }
		100% { background-color: transparent; }
	}
	.capsule-born {
		animation: capsule-born 1.6s ease-out forwards;
	}

	/* Left accent stripe — slides in on row hover */
	.row-stripe {
		transform: scaleY(0);
		transform-origin: center;
		transition: transform 0.18s cubic-bezier(0.25, 1, 0.5, 1);
	}
	.capsule-row:hover .row-stripe {
		transform: scaleY(1);
	}
</style>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<svelte:window onclick={handleClickOutside} onkeydown={(e) => { if (e.key === 'Escape') openMenuId = null; }} />

<svelte:head>
	<title>Wrenn — Capsules</title>
</svelte:head>

<div class="flex h-screen overflow-hidden">
	<Sidebar bind:collapsed />

	<div class="flex flex-1 flex-col overflow-hidden">
		<main class="flex-1 overflow-y-auto bg-[var(--color-bg-0)]">
			<!-- Header area -->
			<div class="px-7 pt-8">
				<!-- Top row: title + status chip -->
				<div class="flex items-center justify-between">
					<div>
						<h1 class="font-serif text-page tracking-[-0.02em] text-[var(--color-text-bright)]">
							Capsules
						</h1>
						<p class="mt-2 text-ui text-[var(--color-text-secondary)]">
							Isolated VMs. Start cold in under a second — pause, snapshot, or destroy at will.
						</p>
					</div>

					<div class="flex items-center gap-3">
						<!-- Status chip -->
						<div
							class="flex items-center gap-2.5 rounded-[var(--radius-card)] border border-[var(--color-accent)]/20 bg-[var(--color-bg-2)] px-3.5 py-2"
						>
							<span class="relative flex h-[8px] w-[8px]">
								<span
									class="animate-status-ping absolute inline-flex h-full w-full rounded-full bg-[var(--color-accent)]"
								></span>
								<span class="relative inline-flex h-[8px] w-[8px] rounded-full bg-[var(--color-accent)]"></span>
							</span>
							<span class="font-mono text-body font-semibold text-[var(--color-accent-bright)]">{runningCount}</span>
							<span class="text-ui text-[var(--color-text-secondary)]">running now</span>
						</div>
					</div>
				</div>

				<!-- Tab bar -->
				<div class="mt-5 flex gap-1 border-b border-[var(--color-border)]">
					<button
						onclick={() => (activeTab = 'list')}
						class="flex items-center gap-2 border-b-2 px-4 py-2.5 text-ui font-medium transition-colors duration-150 {activeTab === 'list'
							? 'border-[var(--color-accent)] text-[var(--color-accent-bright)]'
							: 'border-transparent text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]'}"
					>
						<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
							<line x1="8" y1="6" x2="21" y2="6" /><line x1="8" y1="12" x2="21" y2="12" /><line x1="8" y1="18" x2="21" y2="18" />
							<line x1="3" y1="6" x2="3.01" y2="6" /><line x1="3" y1="12" x2="3.01" y2="12" /><line x1="3" y1="18" x2="3.01" y2="18" />
						</svg>
						List
					</button>
					<button
						onclick={() => (activeTab = 'stats')}
						class="flex items-center gap-2 border-b-2 px-4 py-2.5 text-ui font-medium transition-colors duration-150 {activeTab === 'stats'
							? 'border-[var(--color-accent)] text-[var(--color-accent-bright)]'
							: 'border-transparent text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]'}"
					>
						<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
							<polyline points="22 12 18 12 15 21 9 3 6 12 2 12" />
						</svg>
						Stats
					</button>
				</div>
			</div>

			<!-- Tab content -->
			{#if activeTab === 'stats'}
				<div class="p-8 space-y-5" style="animation: fadeUp 0.35s ease both">
					<div class="flex overflow-hidden rounded-[var(--radius-card)] border border-[var(--color-border)]">
						{@render metricCell('Concurrent Capsules', String(runningCount), '5-sec avg', 'limit: 20', true)}
						{@render metricCell('Start Rate / Second', '0.000', '5-sec avg', null, true)}
						{@render metricCell('Peak Concurrent', String(runningCount), '30-day max', 'limit: 20', false)}
					</div>

					{@render chartCard('Concurrent Capsules', String(runningCount), 'average')}
					{@render chartCard('Start Rate Per Second', '0.000', 'average')}
				</div>
			{:else}
				<div class="p-8" style="animation: fadeUp 0.35s ease both">
					<!-- Search bar + controls -->
					<div class="mb-4 flex items-center gap-3">
						<div class="relative flex-1 max-w-[300px]">
							<svg class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-[var(--color-text-muted)]" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
								<circle cx="11" cy="11" r="8" /><line x1="21" y1="21" x2="16.65" y2="16.65" />
							</svg>
							<input
								type="text"
								placeholder="Search by ID..."
								bind:value={searchQuery}
								class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-2)] py-2 pl-9 pr-3 font-mono text-ui text-[var(--color-text-bright)] outline-none placeholder:text-[var(--color-text-muted)] transition-colors duration-150 focus:border-[var(--color-accent)]"
							/>
						</div>
						<span class="text-ui text-[var(--color-text-secondary)]">{filteredCapsules.length} total</span>

						<div class="flex-1"></div>

						<!-- Refresh button -->
						<button
							onclick={fetchCapsules}
							disabled={spinning}
							class="flex h-8 w-8 items-center justify-center rounded-[var(--radius-button)] border border-[var(--color-border)] text-[var(--color-text-tertiary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-secondary)] disabled:opacity-50"
							title="Refresh"
						>
							<svg
								class={spinning ? 'refresh-spin' : ''}
								width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"
							>
								<polyline points="23 4 23 10 17 10" />
								<path d="M20.49 15a9 9 0 1 1-2.12-9.36L23 10" />
							</svg>
						</button>

						<!-- Auto-refresh countdown toggle -->
						<button
							onclick={toggleAutoRefresh}
							class="flex items-center gap-1.5 rounded-[var(--radius-button)] border px-2.5 py-1.5 font-mono text-label transition-colors duration-150
								{autoRefresh
									? 'border-[var(--color-accent)]/30 text-[var(--color-accent-mid)] hover:border-[var(--color-accent)]/50'
									: 'border-[var(--color-border)] text-[var(--color-text-muted)] hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-secondary)]'}"
							title={autoRefresh ? 'Click to disable auto-refresh' : 'Click to enable auto-refresh (30s)'}
						>
							{#if autoRefresh}
								<!-- Radial progress ring — drains as countdown ticks down -->
								<svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
									<circle cx="8" cy="8" r="5" stroke="var(--color-accent-glow-mid)" stroke-width="1.5" />
									<circle
										cx="8" cy="8" r="5"
										stroke="var(--color-accent)"
										stroke-width="1.5"
										stroke-linecap="round"
										stroke-dasharray="31.416"
										stroke-dashoffset={31.416 * (1 - countdown / REFRESH_INTERVAL)}
										transform="rotate(-90 8 8)"
										style="transition: stroke-dashoffset 1s linear"
									/>
								</svg>
								{countdown}s
							{:else}
								Off
							{/if}
						</button>

						<button
							onclick={() => { showCreateDialog = true; createError = null; }}
							disabled={!auth.teamId}
							title={!auth.teamId ? 'No active team — re-authenticate to create capsules' : undefined}
							class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-accent)] px-4 py-2 text-ui font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0 disabled:pointer-events-none disabled:opacity-40"
						>
							<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
								<line x1="12" y1="5" x2="12" y2="19" /><line x1="5" y1="12" x2="19" y2="12" />
							</svg>
							Launch Capsule
						</button>
					</div>

					{#if error}
						<div class="mb-4 rounded-[var(--radius-card)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-4 py-3 text-ui text-[var(--color-red)]">
							{error}
						</div>
					{/if}

					<!-- Table -->
					<div class="rounded-[var(--radius-card)] border border-[var(--color-border)] overflow-hidden">
						<!-- Table header -->
						<div class="grid grid-cols-[1.6fr_0.8fr_0.5fr_0.5fr_0.6fr_1fr_0.9fr] rounded-t-[var(--radius-card)] border-b border-[var(--color-border)] bg-[var(--color-bg-3)]">
							<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">ID</div>
							<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Template</div>
							{@render sortableHeader('CPU', 'vcpus')}
							{@render sortableHeader('Memory', 'memory_mb')}
							{@render sortableHeader('Idle Timeout', 'timeout_sec')}
							{@render sortableHeader('Started', 'started_at')}
							{@render sortableHeader('Status', 'status')}
						</div>

						{#if loading && capsules.length === 0}
							<div class="flex items-center justify-center py-16">
								<div class="flex items-center gap-3 text-ui text-[var(--color-text-secondary)]">
									<svg class="animate-spin" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
										<path d="M21 12a9 9 0 1 1-6.219-8.56" />
									</svg>
									Loading capsules...
								</div>
							</div>
						{:else if filteredCapsules.length === 0}
							<div class="flex flex-col items-center justify-center py-[72px]">
								<div class="relative mb-5">
									<div class="absolute inset-0 -m-4 rounded-full" style="background: radial-gradient(circle, rgba(94,140,88,0.08) 0%, transparent 70%)"></div>
									<div class="relative flex h-14 w-14 items-center justify-center rounded-[var(--radius-card)] border border-[var(--color-accent)]/20 bg-[var(--color-bg-3)]" style="animation: iconFloat 4s ease-in-out infinite">
										<svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="var(--color-accent-mid)" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
											<rect x="2" y="3" width="20" height="14" rx="2" />
											<line x1="8" y1="21" x2="16" y2="21" />
											<line x1="12" y1="17" x2="12" y2="21" />
										</svg>
									</div>
								</div>
								<p class="font-serif text-heading tracking-[-0.02em] text-[var(--color-text-bright)]">
									No capsules yet
								</p>
								<p class="mt-1.5 text-ui text-[var(--color-text-tertiary)]">
									Each capsule is an isolated VM. Launch one to get started.
								</p>
								<button
									onclick={() => { showCreateDialog = true; createError = null; }}
									disabled={!auth.teamId}
								class="mt-6 flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-accent)] px-5 py-2.5 text-ui font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0 disabled:pointer-events-none disabled:opacity-40"
								>
									Launch a Capsule
									<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
										<line x1="12" y1="5" x2="12" y2="19" /><line x1="5" y1="12" x2="19" y2="12" />
									</svg>
								</button>
							</div>
						{:else}
							{#each filteredCapsules as capsule, i (capsule.id)}
								{@const stripeColor = capsule.status === 'running' ? 'bg-[var(--color-accent)]' : capsule.status === 'paused' ? 'bg-[var(--color-amber)]' : 'bg-[var(--color-text-muted)]'}
								<div
									class="capsule-row relative grid grid-cols-[1.6fr_0.8fr_0.5fr_0.5fr_0.6fr_1fr_0.9fr] items-center overflow-hidden border-b border-[var(--color-border)] transition-colors duration-150 hover:bg-[var(--color-bg-3)] last:border-b-0 {newCapsuleId === capsule.id ? 'capsule-born' : ''}"
									style="animation: fadeUp 0.35s ease both; animation-delay: {i * 40}ms"
								>
									<!-- Left accent stripe — slides in on hover, color-keyed to status -->
									<div class="row-stripe pointer-events-none absolute left-0 top-0 h-full w-0.5 {stripeColor}"></div>

									<!-- ID with status dot -->
									<div class="flex items-center gap-2.5 px-5 py-4">
										{#if capsule.status === 'running'}
											<span class="relative flex h-[6px] w-[6px] shrink-0">
												<span class="animate-status-ping absolute inline-flex h-full w-full rounded-full bg-[var(--color-accent)]"></span>
												<span class="relative inline-flex h-[6px] w-[6px] rounded-full bg-[var(--color-accent)]"></span>
											</span>
										{:else if capsule.status === 'paused'}
											<span class="inline-flex h-[6px] w-[6px] shrink-0 rounded-full bg-[var(--color-amber)]"></span>
										{:else}
											<span class="inline-flex h-[6px] w-[6px] shrink-0 rounded-full bg-[var(--color-text-muted)]"></span>
										{/if}
										{#if searchQuery && capsule.id.toLowerCase().includes(searchQuery.toLowerCase())}
											{@const matchIdx = capsule.id.toLowerCase().indexOf(searchQuery.toLowerCase())}
											<span class="font-mono text-ui text-[var(--color-text-bright)]">{capsule.id.slice(0, matchIdx)}<mark class="rounded-[2px] bg-[var(--color-accent-glow-mid)] px-0.5 text-[var(--color-accent-bright)] not-italic">{capsule.id.slice(matchIdx, matchIdx + searchQuery.length)}</mark>{capsule.id.slice(matchIdx + searchQuery.length)}</span>
										{:else}
											<span class="font-mono text-ui text-[var(--color-text-bright)]">{capsule.id}</span>
										{/if}
									</div>

									<!-- Template -->
									<div class="min-w-0 px-5 py-4">
										<span class="block truncate text-ui text-[var(--color-text-secondary)]">{capsule.template}</span>
									</div>

									<!-- CPU -->
									<div class="px-5 py-4">
										<span class="font-mono text-ui text-[var(--color-text-secondary)]">{capsule.vcpus}</span>
									</div>

									<!-- Memory -->
									<div class="px-5 py-4">
										<span class="font-mono text-ui text-[var(--color-text-secondary)]">{capsule.memory_mb}MB</span>
									</div>

									<!-- Idle Timeout -->
									<div class="px-5 py-4">
										<span class="font-mono text-ui text-[var(--color-text-secondary)]">{capsule.timeout_sec ? `${capsule.timeout_sec}s` : '—'}</span>
									</div>

									<!-- Started -->
									<div class="px-5 py-4">
										<span class="text-ui text-[var(--color-text-secondary)]" title={capsule.started_at ?? ''}>{formatTime(capsule.started_at)}</span>
										{#if capsule.last_active_at}
											<span class="ml-1.5 text-label text-[var(--color-text-muted)]">{timeAgo(capsule.last_active_at)}</span>
										{/if}
									</div>

									<!-- Status button with popover -->
									<div class="relative px-5 py-4 status-menu-container">
										{#if actionLoading === capsule.id}
											<span class="inline-flex items-center gap-1.5 text-ui text-[var(--color-text-secondary)]">
												<svg class="animate-spin" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
													<path d="M21 12a9 9 0 1 1-6.219-8.56" />
												</svg>
											</span>
										{:else}
											<button
												onclick={(e) => {
													e.stopPropagation();
													if (openMenuId === capsule.id) {
														openMenuId = null;
													} else {
														const rect = (e.currentTarget as HTMLElement).getBoundingClientRect();
														menuPos = { top: rect.bottom + 4, left: rect.right - 180 };
														openMenuId = capsule.id;
													}
												}}
												class="inline-flex items-center gap-1.5 rounded-[var(--radius-button)] border px-2.5 py-1 text-label font-semibold uppercase tracking-[0.04em] transition-colors duration-150 {capsule.status === 'running' ? 'border-[var(--color-accent)]/40 bg-[var(--color-accent-glow)] text-[var(--color-accent-mid)] hover:border-[var(--color-accent)]/70 hover:text-[var(--color-accent-bright)]' : capsule.status === 'paused' ? 'border-[var(--color-amber)]/30 bg-[var(--color-amber)]/5 text-[var(--color-amber)] hover:border-[var(--color-amber)]/60' : 'border-[var(--color-border)] bg-[var(--color-bg-2)] text-[var(--color-text-secondary)] hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)]'}"
											>
												{capsule.status}
												<svg
													class="transition-transform duration-150 {openMenuId === capsule.id ? 'rotate-180' : ''}"
													width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"
												>
													<polyline points="6 9 12 15 18 9" />
												</svg>
											</button>

										{/if}
									</div>
								</div>
							{/each}
						{/if}
					</div>
				</div>
			{/if}
		</main>

		<!-- Status bar -->
		<footer
			class="flex h-7 shrink-0 items-center justify-end border-t border-[var(--color-border)] bg-[var(--color-bg-1)] px-7"
		>
			<div class="flex items-center gap-1.5">
				<span class="relative flex h-[5px] w-[5px]">
					<span class="animate-status-ping absolute inline-flex h-full w-full rounded-full bg-[var(--color-accent)]"></span>
					<span class="relative inline-flex h-[5px] w-[5px] rounded-full bg-[var(--color-accent)]"></span>
				</span>
				<span class="font-mono text-label uppercase tracking-[0.04em] text-[var(--color-text-secondary)]">All systems operational</span>
			</div>
		</footer>
	</div>
</div>

<!-- Fixed-position status popover menu -->
{#if openMenuId}
	{@const openCapsule = capsules.find((c) => c.id === openMenuId)}
	{#if openCapsule}
		<div
			class="fixed z-50 w-[180px] overflow-hidden rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] py-1"
			style="top: {menuPos.top}px; left: {menuPos.left}px; animation: fadeUp 0.15s ease both"
		>
			{#if openCapsule.status === 'running'}
				<button
					onclick={() => handlePause(openCapsule.id)}
					class="flex w-full items-center gap-2.5 px-3 py-2 text-meta text-[var(--color-text-secondary)] transition-colors duration-150 hover:bg-[var(--color-bg-3)] hover:text-[var(--color-text-primary)]"
				>
					<svg width="13" height="13" viewBox="0 0 24 24" fill="currentColor" class="shrink-0">
						<rect x="6" y="4" width="4" height="16" rx="1" />
						<rect x="14" y="4" width="4" height="16" rx="1" />
					</svg>
					Pause
				</button>
				<button
					onclick={() => handlePauseAndSnapshot(openCapsule)}
					class="flex w-full items-center gap-2.5 px-3 py-2 text-meta text-[var(--color-text-secondary)] transition-colors duration-150 hover:bg-[var(--color-bg-3)] hover:text-[var(--color-text-primary)]"
				>
					<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="shrink-0">
						<path d="M14.5 4h-5L7 7H2v13a2 2 0 002 2h16a2 2 0 002-2V7h-5l-2.5-3z" />
						<circle cx="12" cy="15" r="3" />
					</svg>
					Pause & Snapshot
				</button>
			{:else if openCapsule.status === 'paused'}
				<button
					onclick={() => handleResume(openCapsule.id)}
					class="flex w-full items-center gap-2.5 px-3 py-2 text-meta text-[var(--color-text-secondary)] transition-colors duration-150 hover:bg-[var(--color-bg-3)] hover:text-[var(--color-text-primary)]"
				>
					<svg width="13" height="13" viewBox="0 0 24 24" fill="currentColor" class="shrink-0">
						<polygon points="5 3 19 12 5 21 5 3" />
					</svg>
					Resume
				</button>
				<button
					onclick={() => handleSnapshot(openCapsule)}
					class="flex w-full items-center gap-2.5 px-3 py-2 text-meta text-[var(--color-text-secondary)] transition-colors duration-150 hover:bg-[var(--color-bg-3)] hover:text-[var(--color-text-primary)]"
				>
					<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="shrink-0">
						<path d="M14.5 4h-5L7 7H2v13a2 2 0 002 2h16a2 2 0 002-2V7h-5l-2.5-3z" />
						<circle cx="12" cy="15" r="3" />
					</svg>
					Snapshot
				</button>
			{/if}
			<div class="my-1 border-t border-[var(--color-border)]"></div>
			<button
				onclick={() => { const target = openCapsule; openMenuId = null; destroyError = null; destroyTarget = target; }}
				class="flex w-full items-center gap-2.5 px-3 py-2 text-meta text-[var(--color-red)] transition-colors duration-150 hover:bg-[var(--color-red)]/5"
			>
				<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="shrink-0">
					<polyline points="3 6 5 6 21 6" />
					<path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2" />
				</svg>
				Destroy
			</button>
		</div>
	{/if}
{/if}

<!-- Snapshot Dialog -->
{#if snapshotTarget}
	<div class="fixed inset-0 z-50 flex items-center justify-center">
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="absolute inset-0 bg-black/60"
			onclick={() => { if (!snapshotting) snapshotTarget = null; }}
			onkeydown={(e) => { if (e.key === 'Escape' && !snapshotting) snapshotTarget = null; }}
		></div>

		<div class="relative w-full max-w-[420px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] overflow-hidden" style="animation: fadeUp 0.2s ease both; box-shadow: var(--shadow-dialog)">
			<!-- Header band -->
			<div class="flex items-center gap-4 border-b border-[var(--color-border)] bg-[var(--color-bg-3)] px-6 py-5">
				<div class="flex h-10 w-10 shrink-0 items-center justify-center rounded-[var(--radius-input)] bg-[var(--color-accent)]/15 text-[var(--color-accent)] shadow-[0_0_12px_var(--color-accent-glow)]">
					<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round">
						<path d="M14.5 4h-5L7 7H2v13a2 2 0 002 2h16a2 2 0 002-2V7h-5l-2.5-3z" />
						<circle cx="12" cy="15" r="3" />
					</svg>
				</div>
				<div>
					<h2 class="font-serif text-heading tracking-[-0.02em] text-[var(--color-text-bright)]">Capture snapshot</h2>
					<p class="mt-0.5 text-meta text-[var(--color-text-muted)] font-mono">{snapshotTarget.capsule.id}</p>
				</div>
			</div>

			<div class="px-6 pt-5 pb-6 space-y-4">
				{#if snapshotTarget.pauseFirst}
					<div class="flex items-start gap-2.5 rounded-[var(--radius-input)] border border-[var(--color-amber)]/25 bg-[var(--color-amber)]/8 px-3 py-2.5">
						<svg class="mt-px shrink-0 text-[var(--color-amber)]" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
							<path d="M10.29 3.86L1.82 18a2 2 0 001.71 3h16.94a2 2 0 001.71-3L13.71 3.86a2 2 0 00-3.42 0z" />
							<line x1="12" y1="9" x2="12" y2="13" />
							<line x1="12" y1="17" x2="12.01" y2="17" />
						</svg>
						<p class="text-meta text-[var(--color-amber)] leading-relaxed">This capsule will be <strong class="font-semibold">paused first</strong> — memory state is captured at rest.</p>
					</div>
				{:else}
					<p class="text-ui text-[var(--color-text-tertiary)]">The capsule's current memory state will be captured and stored as a reusable snapshot.</p>
				{/if}

				{#if snapshotError}
					<div class="rounded-[var(--radius-input)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-3 py-2 text-meta text-[var(--color-red)]">
						{snapshotError}
					</div>
				{/if}

				<div>
					<div class="mb-1.5 flex items-baseline justify-between">
						<label class="text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]" for="snapshot-name">Snapshot name</label>
						<span class="text-meta text-[var(--color-text-muted)]">optional</span>
					</div>
					<input
						id="snapshot-name"
						type="text"
						bind:value={snapshotName}
						disabled={snapshotting}
						class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-4)] px-3 py-2 font-mono text-ui text-[var(--color-text-bright)] outline-none placeholder:text-[var(--color-text-muted)] transition-colors duration-150 focus:border-[var(--color-accent)] disabled:opacity-50"
						placeholder="e.g. after-apt-install, pre-migration"
						onkeydown={(e) => { if (e.key === 'Enter' && !snapshotting) handleSnapshotConfirm(); }}
					/>
					<p class="mt-1.5 text-meta text-[var(--color-text-muted)]">Leave blank to use an auto-generated name.</p>
				</div>

				<div class="flex justify-end gap-3 pt-1">
					<button
						onclick={() => { snapshotTarget = null; }}
						disabled={snapshotting}
						class="rounded-[var(--radius-button)] border border-[var(--color-border)] px-4 py-2 text-ui text-[var(--color-text-secondary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)] disabled:opacity-50"
					>
						Cancel
					</button>
					<button
						onclick={handleSnapshotConfirm}
						disabled={snapshotting}
						class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-accent)] px-5 py-2 text-ui font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0 disabled:opacity-50 disabled:hover:translate-y-0"
					>
						{#if snapshotting}
							<svg class="animate-spin" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
								<path d="M21 12a9 9 0 1 1-6.219-8.56" />
							</svg>
							Capturing...
						{:else}
							Capture snapshot
						{/if}
					</button>
				</div>
			</div>
		</div>
	</div>
{/if}

<!-- Create Capsule Dialog -->
{#if showCreateDialog}
	<div class="fixed inset-0 z-50 flex items-center justify-center">
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="absolute inset-0 bg-black/60"
			onclick={() => { if (!creating) showCreateDialog = false; }}
			onkeydown={(e) => { if (e.key === 'Escape' && !creating) showCreateDialog = false; }}
		></div>

		<div class="relative w-full max-w-[420px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] p-6" style="animation: fadeUp 0.2s ease both; box-shadow: var(--shadow-dialog)">
			<h2 class="font-serif text-heading tracking-[-0.02em] text-[var(--color-text-bright)]">Launch Capsule</h2>
			<p class="mt-1 text-ui text-[var(--color-text-tertiary)]">Configure resources and launch. The VM will be ready in under a second.</p>

			{#if createError}
				<div class="mt-4 rounded-[var(--radius-input)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-3 py-2 text-meta text-[var(--color-red)]">
					{createError}
				</div>
			{/if}

			<div class="mt-5 space-y-4">
				<div>
					<label class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]" for="create-template">Template</label>
					<input
						id="create-template"
						type="text"
						bind:value={createForm.template}
						class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-4)] px-3 py-2 font-mono text-ui text-[var(--color-text-bright)] outline-none placeholder:text-[var(--color-text-muted)] transition-colors duration-150 focus:border-[var(--color-accent)]"
						placeholder="minimal"
					/>
				</div>

				<div class="grid grid-cols-2 gap-3">
					<div>
						<label class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]" for="create-vcpus">vCPUs</label>
						<input
							id="create-vcpus"
							type="number"
							min="1"
							max="8"
							bind:value={createForm.vcpus}
							class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-4)] px-3 py-2 font-mono text-ui text-[var(--color-text-bright)] outline-none transition-colors duration-150 focus:border-[var(--color-accent)]"
						/>
					</div>
					<div>
						<label class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]" for="create-memory">Memory (MB)</label>
						<input
							id="create-memory"
							type="number"
							min="128"
							max="8192"
							step="128"
							bind:value={createForm.memory_mb}
							class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-4)] px-3 py-2 font-mono text-ui text-[var(--color-text-bright)] outline-none transition-colors duration-150 focus:border-[var(--color-accent)]"
						/>
					</div>
				</div>

				<div>
					<label class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]" for="create-timeout">Idle timeout (seconds — 0 = never pause)</label>
					<input
						id="create-timeout"
						type="number"
						min="0"
						bind:value={createForm.timeout_sec}
						class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-4)] px-3 py-2 font-mono text-ui text-[var(--color-text-bright)] outline-none transition-colors duration-150 focus:border-[var(--color-accent)]"
					/>
				</div>
			</div>

			<div class="mt-6 flex justify-end gap-3">
				<button
					onclick={() => { showCreateDialog = false; }}
					disabled={creating}
					class="rounded-[var(--radius-button)] border border-[var(--color-border)] px-4 py-2 text-ui text-[var(--color-text-secondary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)] disabled:opacity-50"
				>
					Cancel
				</button>
				<button
					onclick={handleCreate}
					disabled={creating}
					class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-accent)] px-5 py-2 text-ui font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0 disabled:opacity-50 disabled:hover:translate-y-0"
				>
					{#if creating}
						<svg class="animate-spin" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
							<path d="M21 12a9 9 0 1 1-6.219-8.56" />
						</svg>
						Launching...
					{:else}
						Launch
					{/if}
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Destroy Confirmation Dialog -->
{#if destroyTarget}
	<div class="fixed inset-0 z-50 flex items-center justify-center">
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="absolute inset-0 bg-black/60"
			onclick={() => { if (!destroying) destroyTarget = null; }}
			onkeydown={(e) => { if (e.key === 'Escape' && !destroying) destroyTarget = null; }}
		></div>

		<div class="relative w-full max-w-[380px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] p-6" style="animation: fadeUp 0.2s ease both">
			<h2 class="font-serif text-heading tracking-[-0.02em] text-[var(--color-text-bright)]">Destroy Capsule</h2>
			<p class="mt-2 text-ui text-[var(--color-text-tertiary)]">
				Terminate <span class="font-mono text-[var(--color-text-secondary)]">{destroyTarget.id}</span> and destroy all data inside it. This cannot be undone.
			</p>

			{#if destroyError}
				<div class="mt-4 rounded-[var(--radius-input)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-3 py-2 text-meta text-[var(--color-red)]">
					{destroyError}
				</div>
			{/if}

			<div class="mt-6 flex justify-end gap-3">
				<button
					onclick={() => { destroyTarget = null; }}
					disabled={destroying}
					class="rounded-[var(--radius-button)] border border-[var(--color-border)] px-4 py-2 text-ui text-[var(--color-text-secondary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)] disabled:opacity-50"
				>
					Cancel
				</button>
				<button
					onclick={handleDestroy}
					disabled={destroying}
					class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-red)] px-5 py-2 text-ui font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0 disabled:opacity-50 disabled:hover:translate-y-0"
				>
					{#if destroying}
						<svg class="animate-spin" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
							<path d="M21 12a9 9 0 1 1-6.219-8.56" />
						</svg>
						Destroying...
					{:else}
						Destroy
					{/if}
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Sortable header snippet -->
{#snippet sortableHeader(label: string, key: SortKey)}
	<button
		onclick={() => toggleSort(key)}
		class="flex items-center gap-1 px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)] transition-colors duration-150 hover:text-[var(--color-text-secondary)]"
	>
		{label}
		{#if sortKey === key}
			<svg
				class="transition-transform duration-150 {sortDir === 'desc' ? 'rotate-180' : ''}"
				width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"
			>
				<polyline points="18 15 12 9 6 15" />
			</svg>
		{/if}
	</button>
{/snippet}

{#snippet metricCell(label: string, value: string, sublabel: string, extra: string | null, hasBorderRight: boolean)}
	<div class="flex-1 bg-[var(--color-bg-2)] px-5 py-5 transition-colors duration-150 hover:bg-[var(--color-bg-3)] {hasBorderRight ? 'border-r border-[var(--color-border)]' : ''}">
		<div class="flex items-center gap-2">
			<span class="text-meta font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]">{label}</span>
			<span class="rounded-[3px] bg-[var(--color-accent-glow-mid)] px-1.5 py-0.5 text-badge font-semibold uppercase tracking-[0.04em] text-[var(--color-accent-mid)]">
				<span class="mr-0.5 inline-block h-[5px] w-[5px] rounded-full bg-[var(--color-accent)]" style="animation: wrenn-glow 2.5s ease-in-out infinite"></span>
				Live
			</span>
		</div>
		<div class="mt-1 font-serif text-[2.571rem] tracking-[-0.04em] text-[var(--color-text-bright)]">{value}</div>
		<div class="mt-1 flex items-center gap-1.5 text-label text-[var(--color-text-tertiary)]">
			<span>{sublabel}</span>
			{#if extra}
				<span class="text-[var(--color-text-muted)]">|</span>
				<span class="font-mono text-[var(--color-text-muted)]">{extra}</span>
			{/if}
		</div>
	</div>
{/snippet}

{#snippet chartCard(label: string, value: string, sublabel: string)}
	<div class="rounded-[var(--radius-card)] border border-[var(--color-border)] bg-[var(--color-bg-2)]">
		<div class="flex items-center justify-between px-5 pt-5 pb-3">
			<div>
				<div class="text-meta font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]">{label}</div>
				<div class="mt-0.5 flex items-baseline gap-2">
					<span class="font-serif text-[2.143rem] tracking-[-0.04em] text-[var(--color-text-bright)]">{value}</span>
					<span class="text-ui text-[var(--color-text-secondary)]">{sublabel}</span>
					<span class="rounded-[3px] bg-[var(--color-accent-glow-mid)] px-1.5 py-0.5 text-badge font-semibold uppercase tracking-[0.04em] text-[var(--color-accent-mid)]">
						<span class="mr-0.5 inline-block h-[5px] w-[5px] rounded-full bg-[var(--color-accent)]" style="animation: wrenn-glow 2.5s ease-in-out infinite"></span>
						Live
					</span>
				</div>
			</div>

			<div class="flex overflow-hidden rounded-[var(--radius-button)] border border-[var(--color-border)]">
				{#each ['5m', '1H', '6H', '24H', '30D'] as range, i}
					<button
						class="px-2.5 py-1 font-mono text-label transition-colors duration-150 {range === '1H'
							? 'bg-[var(--color-bg-5)] text-[var(--color-text-bright)]'
							: 'text-[var(--color-text-tertiary)] hover:text-[var(--color-text-secondary)]'} {i > 0
							? 'border-l border-[var(--color-border)]'
							: ''}"
					>
						{range}
					</button>
				{/each}
			</div>
		</div>

		<div class="relative h-[200px] px-5 pb-3">
			<div class="absolute left-0 top-0 flex h-full w-12 flex-col justify-between py-1 text-right">
				<span class="font-mono text-badge text-[var(--color-text-muted)]">4</span>
				<span class="font-mono text-badge text-[var(--color-text-muted)]">3</span>
				<span class="font-mono text-badge text-[var(--color-text-muted)]">2</span>
				<span class="font-mono text-badge text-[var(--color-text-muted)]">1</span>
				<span class="font-mono text-badge text-[var(--color-text-muted)]">0</span>
			</div>

			<svg class="ml-8 h-full w-[calc(100%-2rem)]" viewBox="0 0 400 180" preserveAspectRatio="none">
				{#each [0, 45, 90, 135, 180] as y}
					<line x1="0" y1={y} x2="400" y2={y} stroke="var(--color-border)" stroke-width="0.5" stroke-dasharray="4 4" />
				{/each}
				<line x1="0" y1="180" x2="400" y2="180" stroke="var(--color-accent)" stroke-width="1.5" />
			</svg>

			<div class="ml-8 flex justify-between pt-2">
				{#each ['03:01', '03:02', '03:03', '03:04', '03:05'] as t}
					<span class="font-mono text-badge text-[var(--color-text-muted)]">{t}</span>
				{/each}
			</div>
		</div>
	</div>
{/snippet}
