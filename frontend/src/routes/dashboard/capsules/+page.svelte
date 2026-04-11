<script lang="ts">
	import CreateCapsuleDialog from '$lib/components/CreateCapsuleDialog.svelte';
	import SnapshotDialog from '$lib/components/SnapshotDialog.svelte';
	import DestroyDialog from '$lib/components/DestroyDialog.svelte';
	import CopyButton from '$lib/components/CopyButton.svelte';
	import { capsuleRunningCount } from '$lib/capsule-store.svelte';
	import { onMount } from 'svelte';
	import { toast } from '$lib/toast.svelte';
	import { auth } from '$lib/auth.svelte';
	import {
		listCapsules,
		pauseCapsule,
		resumeCapsule,
		type Capsule
	} from '$lib/api/capsules';

	const REFRESH_INTERVAL = 30;
	const SPIN_DURATION = 600;

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

	// Snapshot dialog state
	let snapshotTarget = $state<{ capsule: Capsule; pauseFirst: boolean } | null>(null);

	// Destroy confirmation state
	let destroyTarget = $state<Capsule | null>(null);

	// Briefly highlight a newly created capsule row
	let newCapsuleId = $state<string | null>(null);

	// Track whether initial load animation has played (suppress on poll refreshes)
	let initialAnimationDone = $state(false);

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

	$effect(() => {
		capsuleRunningCount.value = capsules.filter((c) => c.status === 'running').length;
	});

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

	async function fetchCapsules(manual = false) {
		const wasEmpty = capsules.length === 0;
		if (wasEmpty) loading = true;

		if (manual) {
			spinning = true;
			var spinTimer = new Promise<void>((resolve) => setTimeout(resolve, SPIN_DURATION));
		}

		const result = await listCapsules();
		if (result.ok) {
			capsules = result.data;
		}
		loading = false;

		// Mark initial entrance animation as done after first successful fetch
		if (!initialAnimationDone) {
			setTimeout(() => { initialAnimationDone = true; }, 400 + (capsules.length * 40));
		}

		if (autoRefresh) countdown = REFRESH_INTERVAL;

		if (manual) {
			await spinTimer!;
			spinning = false;
		}
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
		snapshotTarget = { capsule, pauseFirst: false };
	}

	function handlePauseAndSnapshot(capsule: Capsule) {
		openMenuId = null;
		snapshotTarget = { capsule, pauseFirst: true };
	}

	function handleSnapshotDone() {
		snapshotTarget = null;
		fetchCapsules();
	}

	function handleDestroyed() {
		if (destroyTarget) {
			const id = destroyTarget.id;
			capsules = capsules.filter((c) => c.id !== id);
		}
		destroyTarget = null;
	}

	function handleCapsuleCreated(capsule: Capsule) {
		capsules = [capsule, ...capsules];
		newCapsuleId = capsule.id;
		setTimeout(() => { newCapsuleId = null; }, 1600);
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

	function fmtTimeout(sec: number): string {
		if (!sec) return 'None';
		if (sec < 60) return `${sec}s`;
		if (sec < 3600) return `${Math.round(sec / 60)}m`;
		return `${Math.round(sec / 3600)}h`;
	}

	function handleClickOutside(event: MouseEvent) {
		if (openMenuId && !(event.target as Element)?.closest('.status-menu-container')) {
			openMenuId = null;
		}
	}

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

	@keyframes capsule-born {
		0%, 25% { background-color: rgba(94, 140, 88, 0.1); }
		100% { background-color: transparent; }
	}
	.capsule-born {
		animation: capsule-born 1.6s ease-out forwards;
	}

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
		<span class="text-ui text-[var(--color-text-secondary)]">{filteredCapsules.length} capsule{filteredCapsules.length !== 1 ? 's' : ''}</span>

		<div class="flex-1"></div>

		<!-- Refresh button -->
		<button
			onclick={() => fetchCapsules(true)}
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
			onclick={() => { showCreateDialog = true; }}
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
		<div class="mb-4 flex items-start gap-3 rounded-[var(--radius-card)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-4 py-3">
			<svg class="mt-0.5 shrink-0 text-[var(--color-red)]" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
				<circle cx="12" cy="12" r="10" /><line x1="12" y1="8" x2="12" y2="12" /><line x1="12" y1="16" x2="12.01" y2="16" />
			</svg>
			<span class="text-ui text-[var(--color-red)]">{error}. Try refreshing the page.</span>
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
					onclick={() => { showCreateDialog = true; }}
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
					style={initialAnimationDone ? '' : `animation: fadeUp 0.35s ease both; animation-delay: ${i * 40}ms`}
				>
					<!-- Left accent stripe -->
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
							<a href="/dashboard/capsules/{capsule.id}" class="font-mono text-ui text-[var(--color-text-bright)] hover:text-[var(--color-accent-bright)] transition-colors duration-150">{capsule.id.slice(0, matchIdx)}<mark class="rounded-[2px] bg-[var(--color-accent-glow-mid)] px-0.5 text-[var(--color-accent-bright)] not-italic">{capsule.id.slice(matchIdx, matchIdx + searchQuery.length)}</mark>{capsule.id.slice(matchIdx + searchQuery.length)}</a>
						{:else}
							<a href="/dashboard/capsules/{capsule.id}" class="font-mono text-ui text-[var(--color-text-bright)] hover:text-[var(--color-accent-bright)] transition-colors duration-150">{capsule.id}</a>
						{/if}
						<CopyButton value={capsule.id} />
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
						<span class="font-mono text-ui text-[var(--color-text-secondary)]">{fmtTimeout(capsule.timeout_sec)}</span>
					</div>

					<!-- Started -->
					<div class="px-5 py-4">
						<span class="text-ui text-[var(--color-text-secondary)]" title={capsule.started_at ?? ''}>{formatTime(capsule.started_at)}</span>
						{#if capsule.last_active_at}
							<span class="ml-1.5 text-label text-[var(--color-text-muted)]">active {timeAgo(capsule.last_active_at)}</span>
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
				onclick={() => { const target = openCapsule; openMenuId = null; destroyTarget = target; }}
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
	<SnapshotDialog
		open={true}
		capsuleId={snapshotTarget.capsule.id}
		pauseFirst={snapshotTarget.pauseFirst}
		onclose={() => { snapshotTarget = null; }}
		onsnapshot={handleSnapshotDone}
	/>
{/if}

<!-- Destroy Dialog -->
{#if destroyTarget}
	<DestroyDialog
		open={true}
		capsuleId={destroyTarget.id}
		onclose={() => { destroyTarget = null; }}
		ondestroyed={handleDestroyed}
	/>
{/if}

<!-- Create Capsule Dialog -->
<CreateCapsuleDialog
	open={showCreateDialog}
	onclose={() => { showCreateDialog = false; }}
	oncreated={handleCapsuleCreated}
/>

{#snippet sortableHeader(label: string, key: SortKey)}
	<button
		onclick={() => toggleSort(key)}
		class="flex items-center gap-1 px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)] transition-colors duration-150 hover:text-[var(--color-text-secondary)]"
	>
		{label}
		{#if sortKey === key}
			<svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" class="text-[var(--color-accent)]">
				{#if sortDir === 'asc'}
					<polyline points="18 15 12 9 6 15" />
				{:else}
					<polyline points="6 9 12 15 18 9" />
				{/if}
			</svg>
		{/if}
	</button>
{/snippet}
