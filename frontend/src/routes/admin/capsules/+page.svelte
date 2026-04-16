<script lang="ts">
	import CreateCapsuleDialog from '$lib/components/CreateCapsuleDialog.svelte';
	import DestroyDialog from '$lib/components/DestroyDialog.svelte';
	import CopyButton from '$lib/components/CopyButton.svelte';
	import { onMount, onDestroy } from 'svelte';
	import { goto } from '$app/navigation';
	import { toast } from '$lib/toast.svelte';
	import {
		listAdminCapsules,
		destroyAdminCapsule,
	} from '$lib/api/admin-capsules';
	import type { Capsule } from '$lib/api/capsules';

	const REFRESH_INTERVAL = 15;
	const SPIN_DURATION = 600;

	let capsules = $state<Capsule[]>([]);
	let loading = $state(true);
	let error = $state<string | null>(null);
	let showCreateDialog = $state(false);
	let searchQuery = $state('');
	let spinning = $state(false);

	// Auto-refresh
	let autoRefresh = $state(true);
	let countdown = $state(REFRESH_INTERVAL);
	let countdownInterval: ReturnType<typeof setInterval> | null = null;
	let refreshInterval: ReturnType<typeof setInterval> | null = null;

	// Sorting
	type SortKey = 'status' | 'vcpus' | 'memory_mb' | 'started_at' | 'template';
	let sortKey = $state<SortKey | null>(null);
	let sortDir = $state<'asc' | 'desc'>('asc');

	// Destroy state
	let destroyTarget = $state<Capsule | null>(null);

	// Animation tracking
	let initialAnimationDone = $state(false);
	let newCapsuleId = $state<string | null>(null);

	let runningCount = $derived(capsules.filter((c) => c.status === 'running').length);

	let filteredCapsules = $derived.by(() => {
		let list = searchQuery
			? capsules.filter((c) =>
				c.id.toLowerCase().includes(searchQuery.toLowerCase()) ||
				c.template.toLowerCase().includes(searchQuery.toLowerCase())
			)
			: [...capsules];

		if (sortKey) {
			const key = sortKey;
			const dir = sortDir === 'asc' ? 1 : -1;
			list.sort((a, b) => {
				if (key === 'status' || key === 'template') {
					return a[key].localeCompare(b[key]) * dir;
				}
				if (key === 'started_at') {
					const ta = a.started_at ? new Date(a.started_at).getTime() : 0;
					const tb = b.started_at ? new Date(b.started_at).getTime() : 0;
					return (ta - tb) * dir;
				}
				return ((a[key] as number) - (b[key] as number)) * dir;
			});
		}

		return list;
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
			if (countdown <= 0) countdown = REFRESH_INTERVAL;
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

		const result = await listAdminCapsules();
		if (result.ok) {
			capsules = result.data;
			error = null;
		} else {
			error = result.error;
		}
		loading = false;

		if (!initialAnimationDone) {
			setTimeout(() => { initialAnimationDone = true; }, 400 + (capsules.length * 40));
		}

		if (autoRefresh) countdown = REFRESH_INTERVAL;

		if (manual) {
			await spinTimer!;
			spinning = false;
		}
	}

	function handleCreated(capsule: Capsule) {
		goto(`/admin/capsules/${capsule.id}`);
	}

	function handleDestroyed() {
		if (destroyTarget) {
			const id = destroyTarget.id;
			capsules = capsules.filter((c) => c.id !== id);
			toast.success('Capsule destroyed');
		}
		destroyTarget = null;
	}

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

	function formatTime(iso: string | null | undefined): string {
		if (!iso) return '\u2014';
		return new Date(iso).toLocaleString([], {
			month: 'short', day: 'numeric',
			hour: '2-digit', minute: '2-digit',
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

	function handleVisibility() {
		if (document.hidden) {
			stopAutoRefresh();
		} else if (autoRefresh) {
			fetchCapsules();
			startAutoRefresh();
		}
	}

	onMount(() => {
		fetchCapsules();
		startAutoRefresh();
		document.addEventListener('visibilitychange', handleVisibility);
	});

	onDestroy(() => {
		stopAutoRefresh();
		document.removeEventListener('visibilitychange', handleVisibility);
	});
</script>

<svelte:head>
	<title>Wrenn Admin — Capsules</title>
</svelte:head>

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

<main class="flex min-w-0 flex-1 flex-col overflow-hidden">
	<!-- Header -->
		<div class="shrink-0 px-8 pt-8 pb-6">
			<div class="flex items-center justify-between">
				<div>
					<h1 class="font-serif text-page leading-none text-[var(--color-text-bright)]">
						Capsules
					</h1>
					<p class="mt-2 text-ui text-[var(--color-text-secondary)]">
						Launch temporary capsules to build and snapshot platform templates.
					</p>
				</div>

				<div class="flex items-center gap-3">
					{#if !loading && runningCount > 0}
						<div class="flex items-center gap-2.5 rounded-[var(--radius-card)] border border-[var(--color-accent)]/20 bg-[var(--color-bg-2)] px-3.5 py-2">
							<span class="relative flex h-[8px] w-[8px]">
								<span class="animate-status-ping absolute inline-flex h-full w-full rounded-full bg-[var(--color-accent)]"></span>
								<span class="relative inline-flex h-[8px] w-[8px] rounded-full bg-[var(--color-accent)]"></span>
							</span>
							<span class="font-mono text-body font-semibold text-[var(--color-accent-bright)]">{runningCount}</span>
							<span class="text-ui text-[var(--color-text-secondary)]">running</span>
						</div>
					{/if}

					<button
						onclick={() => { showCreateDialog = true; }}
						class="group flex items-center gap-2.5 rounded-[var(--radius-button)] bg-[var(--color-accent)] px-5 py-2.5 text-ui font-semibold text-white shadow-sm transition-all duration-200 hover:shadow-[0_0_20px_var(--color-accent-glow-mid)] hover:brightness-115 hover:-translate-y-px active:translate-y-0"
					>
						<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" class="transition-transform duration-200 group-hover:rotate-90"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>
						Launch Capsule
					</button>
				</div>
			</div>
		</div>

		<!-- Content -->
		<div class="flex-1 overflow-y-auto px-8 pb-6" style="animation: fadeUp 0.35s ease both">
			<!-- Toolbar -->
			<div class="mb-4 flex items-center gap-3">
				<div class="relative flex-1 max-w-[300px]">
					<svg class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-[var(--color-text-muted)]" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
						<circle cx="11" cy="11" r="8" /><line x1="21" y1="21" x2="16.65" y2="16.65" />
					</svg>
					<input
						type="text"
						placeholder="Search by ID or template..."
						bind:value={searchQuery}
						class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-2)] py-2 pl-9 pr-3 font-mono text-ui text-[var(--color-text-bright)] outline-none placeholder:text-[var(--color-text-muted)] transition-colors duration-150 focus:border-[var(--color-accent)]"
					/>
				</div>
				<span class="text-ui text-[var(--color-text-secondary)]">{filteredCapsules.length} capsule{filteredCapsules.length !== 1 ? 's' : ''}</span>

				<div class="flex-1"></div>

				<!-- Refresh -->
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

				<!-- Auto-refresh countdown -->
				<button
					onclick={toggleAutoRefresh}
					class="flex items-center gap-1.5 rounded-[var(--radius-button)] border px-2.5 py-1.5 font-mono text-label transition-colors duration-150
						{autoRefresh
							? 'border-[var(--color-accent)]/30 text-[var(--color-accent-mid)] hover:border-[var(--color-accent)]/50'
							: 'border-[var(--color-border)] text-[var(--color-text-muted)] hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-secondary)]'}"
					title={autoRefresh ? 'Click to disable auto-refresh' : `Click to enable auto-refresh (${REFRESH_INTERVAL}s)`}
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
				<!-- Header row -->
				<div class="grid grid-cols-[1.6fr_0.9fr_0.5fr_0.5fr_1fr_0.7fr_0.8fr] rounded-t-[var(--radius-card)] border-b border-[var(--color-border)] bg-[var(--color-bg-3)]">
					<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">ID</div>
					{@render sortableHeader('Template', 'template')}
					{@render sortableHeader('CPU', 'vcpus')}
					{@render sortableHeader('Memory', 'memory_mb')}
					{@render sortableHeader('Started', 'started_at')}
					{@render sortableHeader('Status', 'status')}
					<div class="px-5 py-3 text-right text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Actions</div>
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
				{:else if filteredCapsules.length === 0 && searchQuery}
					<div class="flex flex-col items-center justify-center py-[72px]">
						<div class="relative mb-5">
							<div class="relative flex h-14 w-14 items-center justify-center rounded-[var(--radius-card)] border border-[var(--color-border)] bg-[var(--color-bg-3)]">
								<svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="var(--color-text-muted)" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
									<circle cx="11" cy="11" r="8" /><line x1="21" y1="21" x2="16.65" y2="16.65" />
								</svg>
							</div>
						</div>
						<p class="font-serif text-heading text-[var(--color-text-bright)]">
							No matching capsules
						</p>
						<p class="mt-1.5 text-ui text-[var(--color-text-tertiary)]">
							No capsules match "<span class="font-mono text-[var(--color-text-secondary)]">{searchQuery}</span>".
						</p>
						<button
							onclick={() => { searchQuery = ''; }}
							class="mt-4 rounded-[var(--radius-button)] border border-[var(--color-border)] px-4 py-2 text-ui text-[var(--color-text-secondary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)]"
						>
							Clear search
						</button>
					</div>
				{:else if filteredCapsules.length === 0}
					<div class="flex flex-col items-center justify-center py-[72px]">
						<div class="relative mb-5">
							<div class="absolute inset-0 -m-4 rounded-full" style="background: radial-gradient(circle, rgba(94,140,88,0.08) 0%, transparent 70%)"></div>
							<div class="relative flex h-14 w-14 items-center justify-center rounded-[var(--radius-card)] border border-[var(--color-accent)]/20 bg-[var(--color-bg-3)]" style="animation: iconFloat 4s ease-in-out infinite">
								<svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="var(--color-accent-mid)" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
									<polyline points="4 17 10 11 4 5" /><line x1="12" y1="19" x2="20" y2="19" />
								</svg>
							</div>
						</div>
						<p class="font-serif text-heading text-[var(--color-text-bright)]">
							No capsules
						</p>
						<p class="mt-1.5 max-w-[340px] text-center text-ui text-[var(--color-text-tertiary)]">
							Launch a capsule, configure it interactively, then snapshot it as a platform template.
						</p>
						<button
							onclick={() => { showCreateDialog = true; }}
							class="mt-6 flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-accent)] px-5 py-2.5 text-ui font-semibold text-white shadow-sm transition-all duration-200 hover:shadow-[0_0_20px_var(--color-accent-glow-mid)] hover:brightness-115 hover:-translate-y-px active:translate-y-0"
						>
							Launch Capsule
							<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
								<line x1="12" y1="5" x2="12" y2="19" /><line x1="5" y1="12" x2="19" y2="12" />
							</svg>
						</button>
					</div>
				{:else}
					{#each filteredCapsules as capsule, i (capsule.id)}
						{@const stripeColor = capsule.status === 'running' ? 'bg-[var(--color-accent)]' : capsule.status === 'paused' ? 'bg-[var(--color-amber)]' : capsule.status === 'error' ? 'bg-[var(--color-red)]' : 'bg-[var(--color-text-muted)]'}
						<div
							class="capsule-row relative grid grid-cols-[1.6fr_0.9fr_0.5fr_0.5fr_1fr_0.7fr_0.8fr] items-center overflow-hidden border-b border-[var(--color-border)] transition-colors duration-150 hover:bg-[var(--color-bg-3)] last:border-b-0 {newCapsuleId === capsule.id ? 'capsule-born' : ''}"
							style={initialAnimationDone ? '' : `animation: fadeUp 0.35s ease both; animation-delay: ${i * 40}ms`}
						>
							<!-- Left accent stripe -->
							<div class="row-stripe pointer-events-none absolute left-0 top-0 h-full w-0.5 {stripeColor}"></div>

							<!-- ID -->
							<div class="flex items-center gap-2.5 px-5 py-4">
								{#if capsule.status === 'running'}
									<span class="relative flex h-[6px] w-[6px] shrink-0">
										<span class="animate-status-ping absolute inline-flex h-full w-full rounded-full bg-[var(--color-accent)]"></span>
										<span class="relative inline-flex h-[6px] w-[6px] rounded-full bg-[var(--color-accent)]"></span>
									</span>
								{:else if capsule.status === 'paused'}
									<span class="inline-flex h-[6px] w-[6px] shrink-0 rounded-full bg-[var(--color-amber)]"></span>
								{:else if capsule.status === 'error'}
									<span class="inline-flex h-[6px] w-[6px] shrink-0 rounded-full bg-[var(--color-red)]"></span>
								{:else}
									<span class="inline-flex h-[6px] w-[6px] shrink-0 rounded-full bg-[var(--color-text-muted)]"></span>
								{/if}
								{#if searchQuery && capsule.id.toLowerCase().includes(searchQuery.toLowerCase())}
									{@const matchIdx = capsule.id.toLowerCase().indexOf(searchQuery.toLowerCase())}
									<a href="/admin/capsules/{capsule.id}" class="font-mono text-ui text-[var(--color-text-bright)] hover:text-[var(--color-accent-bright)] transition-colors duration-150">{capsule.id.slice(0, matchIdx)}<mark class="rounded-[2px] bg-[var(--color-accent-glow-mid)] px-0.5 text-[var(--color-accent-bright)] not-italic">{capsule.id.slice(matchIdx, matchIdx + searchQuery.length)}</mark>{capsule.id.slice(matchIdx + searchQuery.length)}</a>
								{:else}
									<a href="/admin/capsules/{capsule.id}" class="font-mono text-ui text-[var(--color-text-bright)] hover:text-[var(--color-accent-bright)] transition-colors duration-150">{capsule.id}</a>
								{/if}
								<CopyButton value={capsule.id} />
							</div>

							<!-- Template -->
							<div class="min-w-0 px-5 py-4">
								<span class="block truncate font-mono text-ui text-[var(--color-text-secondary)]">{capsule.template}</span>
							</div>

							<!-- CPU -->
							<div class="px-5 py-4">
								<span class="font-mono text-ui text-[var(--color-text-secondary)]">{capsule.vcpus}</span>
							</div>

							<!-- Memory -->
							<div class="px-5 py-4">
								<span class="font-mono text-ui text-[var(--color-text-secondary)]">{capsule.memory_mb}MB</span>
							</div>

							<!-- Started -->
							<div class="px-5 py-4">
								<span class="text-ui text-[var(--color-text-secondary)]" title={capsule.started_at ?? ''}>{formatTime(capsule.started_at)}</span>
								{#if capsule.last_active_at}
									<span class="ml-1.5 text-label text-[var(--color-text-muted)]">active {timeAgo(capsule.last_active_at)}</span>
								{/if}
							</div>

							<!-- Status -->
							<div class="px-5 py-4">
								<span
									class="inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-label font-semibold uppercase tracking-[0.05em]"
									style="color: {statusColor(capsule.status)}; background: {statusBg(capsule.status)}; border: 1px solid {statusBorder(capsule.status)}"
								>
									{capsule.status}
								</span>
							</div>

							<!-- Actions -->
							<div class="flex items-center justify-end gap-2 px-5 py-4">
								{#if capsule.status === 'running' || capsule.status === 'paused'}
									<button
										onclick={() => { destroyTarget = capsule; }}
										class="rounded-[var(--radius-button)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/8 px-3 py-1.5 text-meta font-medium text-[var(--color-red)] transition-all duration-150 hover:bg-[var(--color-red)]/15 hover:border-[var(--color-red)]/50"
									>
										Destroy
									</button>
								{/if}
							</div>
						</div>
					{/each}
				{/if}
			</div>
		</div>

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
</main>

<CreateCapsuleDialog
	open={showCreateDialog}
	onclose={() => { showCreateDialog = false; }}
	oncreated={handleCreated}
	templateSource="platform"
/>

{#if destroyTarget}
	<DestroyDialog
		open={true}
		capsuleId={destroyTarget.id}
		onclose={() => { destroyTarget = null; }}
		ondestroyed={handleDestroyed}
		destroyFn={destroyAdminCapsule}
	/>
{/if}

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
