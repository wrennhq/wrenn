<script lang="ts">
	import CopyButton from '$lib/components/CopyButton.svelte';
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { fly } from 'svelte/transition';
	import { cubicIn, cubicOut } from 'svelte/easing';
	import {
		listSnapshots,
		deleteSnapshot,
		createCapsule,
		type Snapshot
	} from '$lib/api/capsules';
	import { formatDate, timeAgo } from '$lib/utils/format';

	// Page tab — Images is disabled/future
	let pageTab = $state<'snapshots' | 'images'>('snapshots');

	// Type filter within snapshots tab
	type TypeFilter = 'all' | 'snapshot' | 'base';
	let typeFilter = $state<TypeFilter>('all');

	// List state
	let snapshots = $state<Snapshot[]>([]);
	let loading = $state(true);
	let error = $state<string | null>(null);

	// Delete state
	let deleteTarget = $state<Snapshot | null>(null);
	let deleting = $state(false);
	let deleteError = $state<string | null>(null);

	// Row dropdown (split button chevron)
	let openDropdownName = $state<string | null>(null);
	let dropdownPos = $state<{ top: number; left: number }>({ top: 0, left: 0 });

	// Launch state
	let launchTarget = $state<Snapshot | null>(null);
	let launchVcpus = $state(1);
	let launchMemoryMb = $state(512);
	let launchTimeoutSec = $state(0);
	let launching = $state(false);
	let launchError = $state<string | null>(null);

	let filteredSnapshots = $derived.by(() => {
		if (typeFilter === 'all') return snapshots;
		return snapshots.filter((s) => s.type === typeFilter);
	});

	// Suppress row fly-transitions after initial load so filter switches are instant.
	let initialLoadDone = $state(false);

	async function fetchSnapshots() {
		loading = true;
		error = null;
		const result = await listSnapshots();
		if (result.ok) {
			snapshots = result.data;
		} else {
			error = result.error;
		}
		loading = false;
		// Allow entrance animations to play on initial load, then suppress
		// them on subsequent filter changes to avoid visual flicker.
		setTimeout(() => { initialLoadDone = true; }, 400);
	}

	async function handleDelete() {
		if (!deleteTarget) return;
		deleting = true;
		deleteError = null;
		const name = deleteTarget.name;
		const result = await deleteSnapshot(name);
		if (result.ok) {
			snapshots = snapshots.filter((s) => s.name !== name);
			deleteTarget = null;
		} else {
			deleteError = result.error;
		}
		deleting = false;
	}

	function openLaunch(snapshot: Snapshot) {
		launchTarget = snapshot;
		launchVcpus = snapshot.vcpus ?? 1;
		launchMemoryMb = snapshot.memory_mb ?? 512;
		launchTimeoutSec = 0;
		launchError = null;
	}

	async function handleLaunch() {
		if (!launchTarget) return;
		launching = true;
		launchError = null;
		const result = await createCapsule({
			template: launchTarget.name,
			vcpus: launchVcpus,
			memory_mb: launchMemoryMb,
			timeout_sec: launchTimeoutSec
		});
		if (result.ok) {
			launchTarget = null;
			goto('/dashboard/capsules');
		} else {
			launchError = result.error;
		}
		launching = false;
	}

	function formatBytes(bytes: number): string {
		if (bytes < 1024) return `${bytes} B`;
		if (bytes < 1024 ** 2) return `${(bytes / 1024).toFixed(1)} KB`;
		if (bytes < 1024 ** 3) return `${(bytes / 1024 ** 2).toFixed(1)} MB`;
		return `${(bytes / 1024 ** 3).toFixed(2)} GB`;
	}

	function emptyHeading(f: TypeFilter): string {
		if (f === 'snapshot') return 'No snapshots yet';
		if (f === 'base') return 'No base images';
		return 'No snapshots yet';
	}

	function emptyDescription(f: TypeFilter): string {
		if (f === 'snapshot') return 'Pause a running capsule, then choose Snapshot to save its state.';
		if (f === 'base') return 'Base images are provided by the Wrenn team. Contact support to request a custom one.';
		return 'Pause a running capsule, then choose Snapshot to save its state. You can launch new capsules from any snapshot.';
	}

	onMount(fetchSnapshots);
</script>

<svelte:head>
	<title>Wrenn — Templates</title>
</svelte:head>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<svelte:window
	onkeydown={(e) => {
		if (e.key === 'Escape') {
			if (openDropdownName) { openDropdownName = null; return; }
			if (deleting || launching) return;
			deleteTarget = null;
			launchTarget = null;
		}
	}}
	onclick={(e) => {
		if (openDropdownName && !(e.target as Element)?.closest('.split-btn-container')) {
			openDropdownName = null;
		}
	}}
/>

	<main class="flex-1 overflow-y-auto bg-[var(--color-bg-0)]">
			<!-- Header -->
			<div class="px-8 pt-8">
				<div class="flex items-start justify-between">
					<div>
						<h1 class="font-serif text-page text-[var(--color-text-bright)]">
							Templates
						</h1>
						<p class="mt-2 text-ui text-[var(--color-text-secondary)]">
							Snapshots capture a running capsule's state. Base images are the starting point for every new capsule. Launch from either.
						</p>
					</div>
				</div>

				<!-- Page-level tabs -->
				<div class="mt-6 flex gap-0 border-b border-[var(--color-border)]">
					<!-- Snapshots tab (active) -->
					<button
						onclick={() => (pageTab = 'snapshots')}
						class="flex items-center gap-2 border-b-2 px-4 py-2.5 text-ui font-medium transition-colors duration-150 {pageTab === 'snapshots'
							? 'border-[var(--color-accent)] text-[var(--color-accent-bright)]'
							: 'border-transparent text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]'}"
					>
						<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
							<line x1="8" y1="6" x2="21" y2="6" /><line x1="8" y1="12" x2="21" y2="12" /><line x1="8" y1="18" x2="21" y2="18" />
							<line x1="3" y1="6" x2="3.01" y2="6" /><line x1="3" y1="12" x2="3.01" y2="12" /><line x1="3" y1="18" x2="3.01" y2="18" />
						</svg>
						List
					</button>

					<!-- Images tab (disabled, coming soon) -->
					<button
						disabled
						title="Coming soon"
						class="flex cursor-not-allowed items-center gap-2 border-b-2 border-transparent px-4 py-2.5 text-ui font-medium opacity-40"
					>
						<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
							<rect x="3" y="3" width="18" height="18" rx="2" ry="2" />
							<circle cx="8.5" cy="8.5" r="1.5" />
							<polyline points="21 15 16 10 5 21" />
						</svg>
						Images
						<span class="rounded-[3px] bg-[var(--color-bg-4)] px-1.5 py-0.5 text-badge font-semibold uppercase tracking-[0.06em] text-[var(--color-text-muted)]">
							Soon
						</span>
					</button>
				</div>
			</div>

			<!-- Snapshots tab content -->
			{#if pageTab === 'snapshots'}
				<div class="p-8" style="animation: fadeUp 0.35s ease both">
					{#if error}
						<div class="mb-4 flex items-start gap-3 rounded-[var(--radius-card)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-4 py-3">
							<svg class="mt-0.5 shrink-0 text-[var(--color-red)]" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
								<circle cx="12" cy="12" r="10" /><line x1="12" y1="8" x2="12" y2="12" /><line x1="12" y1="16" x2="12.01" y2="16" />
							</svg>
							<span class="text-ui text-[var(--color-red)]">{error}. Try refreshing the page.</span>
						</div>
					{/if}

					{#if loading}
						<!-- Skeleton loading — matches table layout -->
						<div class="mb-4 flex items-center justify-between">
							<div class="flex gap-1.5">
								{#each Array(3) as _, i}
									<div class="skeleton h-6 rounded-full px-3" style="width: {[36, 80, 60][i]}px; animation-delay: {i * 80}ms"></div>
								{/each}
							</div>
							<div class="skeleton h-4 w-20 rounded-sm"></div>
						</div>
						<div class="overflow-hidden rounded-[var(--radius-card)] border border-[var(--color-border)] shadow-sm">
							<div class="grid border-b border-[var(--color-border)] bg-[var(--color-bg-0)]/40" style="grid-template-columns: 2fr 1fr 0.7fr 0.9fr 0.8fr 1.3fr 140px">
								<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Name</div>
								<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Type</div>
								<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">vCPUs</div>
								<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Memory</div>
								<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Size</div>
								<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Created</div>
								<div class="px-5 py-3 text-right text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Actions</div>
							</div>
							{#each Array(4) as _, i}
								<div
									class="grid items-center border-b border-[var(--color-border)] last:border-b-0"
									style="grid-template-columns: 2fr 1fr 0.7fr 0.9fr 0.8fr 1.3fr 140px"
								>
									<div class="px-5 py-4"><div class="skeleton h-3 rounded-sm" style="width: {[160, 120, 180, 140][i]}px; animation-delay: {i * 60}ms"></div></div>
									<div class="px-5 py-4"><div class="skeleton h-[18px] w-16 rounded-[3px]" style="animation-delay: {i * 60 + 20}ms"></div></div>
									<div class="px-5 py-4"><div class="skeleton h-3 w-5 rounded-sm" style="animation-delay: {i * 60 + 40}ms"></div></div>
									<div class="px-5 py-4"><div class="skeleton h-3 w-14 rounded-sm" style="animation-delay: {i * 60 + 60}ms"></div></div>
									<div class="px-5 py-4"><div class="skeleton h-3 w-12 rounded-sm" style="animation-delay: {i * 60 + 80}ms"></div></div>
									<div class="px-5 py-4"><div class="skeleton h-3 w-20 rounded-sm" style="animation-delay: {i * 60 + 100}ms"></div></div>
									<div class="flex items-center justify-end px-3 py-3"><div class="skeleton h-7 w-[100px] rounded-[var(--radius-button)]" style="animation-delay: {i * 60 + 120}ms"></div></div>
								</div>
							{/each}
						</div>
					{:else}
						<!-- Filter row -->
						<div class="mb-4 flex items-center justify-between">
							<div class="flex gap-1.5">
								{#each ([['all', 'All', ''], ['snapshot', 'Snapshots', 'var(--color-accent)'], ['base', 'Images', 'var(--color-blue)']] as const) as [val, label, color]}
									<button
										onclick={() => (typeFilter = val)}
										class="flex items-center gap-1.5 rounded-full border px-3 py-1 text-meta font-medium transition-all duration-150 active:scale-95
											{typeFilter === val
												? val === 'all'
													? 'border-[var(--color-border-mid)] bg-[var(--color-bg-5)] text-[var(--color-text-bright)]'
													: val === 'snapshot'
														? 'border-[var(--color-accent)]/30 bg-[var(--color-accent)]/8 text-[var(--color-accent-bright)]'
														: 'border-[var(--color-blue)]/30 bg-[var(--color-blue)]/8 text-[var(--color-blue)]'
												: 'border-[var(--color-border)] bg-[var(--color-bg-3)] text-[var(--color-text-secondary)] hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)]'}"
									>
										{#if val !== 'all'}
											<span
												class="inline-block h-1.5 w-1.5 rounded-full"
												style="background: {color}"
											></span>
										{/if}
										{label}
									</button>
								{/each}
							</div>
							<span class="text-meta text-[var(--color-text-muted)]">
								{filteredSnapshots.length}
								{typeFilter === 'snapshot'
									? filteredSnapshots.length === 1 ? 'snapshot' : 'snapshots'
									: typeFilter === 'base'
										? filteredSnapshots.length === 1 ? 'image' : 'images'
										: filteredSnapshots.length === 1 ? 'item' : 'total'}
							</span>
						</div>

						{#if filteredSnapshots.length === 0}
							<!-- Empty state -->
							<div class="flex flex-col items-center justify-center py-28 text-center">
								<div class="relative mb-7">
									<div class="absolute -inset-3 rounded-2xl bg-[var(--color-accent-glow)] blur-xl"></div>
									<div
										class="empty-icon-float relative flex h-18 w-18 items-center justify-center rounded-xl border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] shadow-card"
									>
										<svg width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.25" stroke-linecap="round" stroke-linejoin="round" class="text-[var(--color-accent-mid)]">
											<path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z" />
											<polyline points="3.27 6.96 12 12.01 20.73 6.96" /><line x1="12" y1="22.08" x2="12" y2="12" />
										</svg>
									</div>
								</div>
								<p class="font-serif text-heading leading-snug text-[var(--color-text-secondary)]">
									{emptyHeading(typeFilter)}
								</p>
								<p class="mt-2 max-w-[320px] text-ui text-[var(--color-text-muted)]">
									{emptyDescription(typeFilter)}
								</p>
								{#if typeFilter === 'all' || typeFilter === 'snapshot'}
									<a
										href="/dashboard/capsules"
										class="mt-6 flex items-center gap-2 rounded-[var(--radius-button)] border border-[var(--color-accent)]/30 bg-[var(--color-accent)]/10 px-4 py-2 text-ui font-medium text-[var(--color-accent-bright)] transition-all duration-200 hover:bg-[var(--color-accent)]/20 hover:border-[var(--color-accent)]/50"
									>
										Go to Capsules
										<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
											<line x1="5" y1="12" x2="19" y2="12" />
											<polyline points="12 5 19 12 12 19" />
										</svg>
									</a>
								{/if}
							</div>
						{:else}
							<!-- Table -->
							<div class="overflow-hidden rounded-[var(--radius-card)] border border-[var(--color-border)] shadow-sm">
								<!-- Header -->
								<div class="grid border-b border-[var(--color-border)] bg-[var(--color-bg-0)]/40" style="grid-template-columns: 2fr 1fr 0.7fr 0.9fr 0.8fr 1.3fr 140px">
									<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Name</div>
									<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Type</div>
									<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">vCPUs</div>
									<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Memory</div>
									<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Size</div>
									<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Created</div>
									<div class="px-5 py-3 text-right text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Actions</div>
								</div>

								<!-- Rows -->
								{#each filteredSnapshots as snapshot, i (snapshot.name)}
									{@const isSnapshot = snapshot.type === 'snapshot'}
									{@const typeColor = isSnapshot ? 'var(--color-accent)' : 'var(--color-blue)'}
									<div
										class="snapshot-row row-item relative grid items-center overflow-hidden border-b border-[var(--color-border)] transition-colors duration-150 last:border-b-0
											{isSnapshot ? 'type-snapshot' : 'type-image'}"
										style="grid-template-columns: 2fr 1fr 0.7fr 0.9fr 0.8fr 1.3fr 140px"
										in:fly={initialLoadDone ? { duration: 0 } : { y: 6, duration: 350, delay: i * 40, easing: cubicOut }}
										out:fly={initialLoadDone ? { duration: 0 } : { x: -12, duration: 180, easing: cubicIn }}
									>
										<!-- Left accent stripe -->
										<div class="row-stripe pointer-events-none absolute left-0 top-0 h-full w-[3px]" style="background: {typeColor}"></div>

										<!-- Name -->
										<div class="min-w-0 px-5 py-4">
											<div class="flex items-center gap-1.5">
												<span class="block truncate font-mono text-ui text-[var(--color-text-bright)]">{snapshot.name}</span>
												<CopyButton value={snapshot.name} />
											</div>
										</div>

										<!-- Type badge -->
										<div class="px-5 py-4">
											{#if isSnapshot}
												<span class="inline-flex items-center gap-1.5 rounded-full border border-[var(--color-accent)]/25 bg-[var(--color-accent)]/8 px-2.5 py-0.5 text-label font-medium text-[var(--color-accent-bright)]">
													<span class="h-1.5 w-1.5 rounded-full bg-[var(--color-accent)]"></span>
													snapshot
												</span>
											{:else}
												<span class="inline-flex items-center gap-1.5 rounded-full border border-[var(--color-blue)]/25 bg-[var(--color-blue)]/8 px-2.5 py-0.5 text-label font-medium text-[var(--color-blue)]">
													<span class="h-1.5 w-1.5 rounded-full bg-[var(--color-blue)]"></span>
													image
												</span>
											{/if}
										</div>

										<!-- vCPUs -->
										<div class="px-5 py-4">
											{#if snapshot.vcpus != null && snapshot.vcpus > 0}
												<span class="flex items-center gap-1.5" title={isSnapshot ? 'Required' : 'Recommended'}>
													<svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke={typeColor} stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="shrink-0 opacity-50">
														<rect x="4" y="4" width="16" height="16" rx="2" /><rect x="9" y="9" width="6" height="6" /><line x1="9" y1="1" x2="9" y2="4" /><line x1="15" y1="1" x2="15" y2="4" /><line x1="9" y1="20" x2="9" y2="23" /><line x1="15" y1="20" x2="15" y2="23" /><line x1="20" y1="9" x2="23" y2="9" /><line x1="20" y1="14" x2="23" y2="14" /><line x1="1" y1="9" x2="4" y2="9" /><line x1="1" y1="14" x2="4" y2="14" />
													</svg>
													<span class="font-mono text-ui text-[var(--color-text-secondary)]">{snapshot.vcpus}</span>
												</span>
											{:else}
												<span class="text-ui text-[var(--color-text-muted)]">—</span>
											{/if}
										</div>

										<!-- Memory -->
										<div class="px-5 py-4">
											{#if snapshot.memory_mb != null && snapshot.memory_mb > 0}
												<span class="flex items-center gap-1.5" title={isSnapshot ? 'Required' : 'Recommended'}>
													<svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke={typeColor} stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="shrink-0 opacity-50">
														<rect x="2" y="6" width="20" height="12" rx="2" /><line x1="6" y1="12" x2="6" y2="12.01" /><line x1="10" y1="12" x2="10" y2="12.01" /><line x1="14" y1="12" x2="14" y2="12.01" /><line x1="18" y1="12" x2="18" y2="12.01" />
													</svg>
													<span class="font-mono text-ui text-[var(--color-text-secondary)]">{snapshot.memory_mb} <span class="text-[var(--color-text-muted)]">MB</span></span>
												</span>
											{:else}
												<span class="text-ui text-[var(--color-text-muted)]">—</span>
											{/if}
										</div>

										<!-- Size -->
										<div class="px-5 py-4">
											<span class="font-mono text-ui text-[var(--color-text-secondary)]">{formatBytes(snapshot.size_bytes)}</span>
										</div>

										<!-- Created -->
										<div class="px-5 py-4" title={formatDate(snapshot.created_at)}>
											<span class="text-ui text-[var(--color-text-secondary)]">{timeAgo(snapshot.created_at)}</span>
										</div>

										<!-- Actions: split button -->
										<div class="flex items-center justify-end px-3 py-3">
											<div class="split-btn-container relative flex items-stretch overflow-hidden rounded-[var(--radius-button)] border border-[var(--color-border-mid)] bg-[var(--color-bg-3)] transition-shadow duration-200 hover:shadow-[0_0_0_1px_var(--color-border-mid),0_0_8px_rgba(94,140,88,0.06)]">
												<!-- Launch part -->
												<button
													onclick={() => openLaunch(snapshot)}
													class="flex items-center px-3 py-1.5 text-meta font-medium text-[var(--color-text-primary)] transition-all duration-150 hover:bg-[var(--color-bg-4)] hover:text-[var(--color-text-bright)] active:scale-95"
												>
													Launch
												</button>
												<!-- Divider -->
												<div class="w-px shrink-0 bg-[var(--color-border-mid)]"></div>
												<!-- Chevron / dropdown trigger -->
												<button
													disabled={snapshot.platform}
													onclick={(e) => {
														e.stopPropagation();
														if (openDropdownName === snapshot.name) {
															openDropdownName = null;
														} else {
															const rect = (e.currentTarget as HTMLElement).getBoundingClientRect();
															dropdownPos = { top: rect.bottom + 4, left: rect.right - 128 };
															openDropdownName = snapshot.name;
														}
													}}
													class="flex items-center px-2 py-1.5 text-[var(--color-text-secondary)] transition-colors duration-150 hover:bg-[var(--color-bg-4)] hover:text-[var(--color-text-bright)] disabled:cursor-not-allowed disabled:opacity-30 disabled:hover:bg-transparent disabled:hover:text-[var(--color-text-secondary)]"
												>
													<svg
														class="transition-transform duration-150 {openDropdownName === snapshot.name ? 'rotate-180' : ''}"
														width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"
													>
														<polyline points="6 9 12 15 18 9" />
													</svg>
												</button>
											</div>
										</div>
									</div>
								{/each}
							</div>

							<p class="mt-3 text-meta text-[var(--color-text-muted)]">
								{filteredSnapshots.length}
								{typeFilter === 'snapshot'
									? filteredSnapshots.length === 1 ? 'snapshot' : 'snapshots'
									: typeFilter === 'base'
										? filteredSnapshots.length === 1 ? 'image' : 'images'
										: filteredSnapshots.length === 1 ? 'item' : 'total'}
								{typeFilter !== 'all' ? '· filtered' : '· total'}
							</p>
						{/if}
					{/if}
				</div>
			{/if}
		</main>

		<!-- Status bar -->
		<footer class="flex h-7 shrink-0 items-center justify-end border-t border-[var(--color-border)] bg-[var(--color-bg-1)] px-8">
			<div class="flex items-center gap-1.5">
				<span
					class="inline-flex h-[5px] w-[5px] rounded-full bg-[var(--color-accent)]"
					style="animation: wrenn-glow 2.4s ease-in-out infinite"
				></span>
				<span class="font-mono text-label uppercase tracking-[0.04em] text-[var(--color-text-secondary)]">All systems operational</span>
			</div>
		</footer>

<!-- Split button dropdown -->
{#if openDropdownName}
	{@const dropdownSnapshot = snapshots.find((s) => s.name === openDropdownName)}
	{#if dropdownSnapshot}
		<div
			class="fixed z-50 w-32 overflow-hidden rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] py-1"
			style="top: {dropdownPos.top}px; left: {dropdownPos.left}px; animation: fadeUp 0.15s ease both"
		>
			{#if !dropdownSnapshot.platform}
				<button
					onclick={(e) => {
						e.stopPropagation();
						const target = snapshots.find((s) => s.name === openDropdownName);
						openDropdownName = null;
						if (target) { deleteTarget = target; deleteError = null; }
					}}
					class="flex w-full items-center gap-2 px-3 py-2 text-meta text-[var(--color-red)] transition-colors duration-150 hover:bg-[var(--color-red)]/5"
				>
					<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="shrink-0">
						<polyline points="3 6 5 6 21 6" />
						<path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2" />
					</svg>
					Delete
				</button>
			{/if}
		</div>
	{/if}
{/if}

<!-- Delete Confirmation Dialog -->
{#if deleteTarget}
	<div class="fixed inset-0 z-50 flex items-center justify-center">
		<div
			class="absolute inset-0 bg-black/60"
			onclick={() => { if (!deleting) deleteTarget = null; }}
			onkeydown={(e) => { if (e.key === 'Escape' && !deleting) deleteTarget = null; }}
		></div>

		<div
			class="relative w-full max-w-[420px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)]"
			style="animation: fadeUp 0.2s ease both; box-shadow: var(--shadow-dialog)"
		>

			<div class="p-6">
			<h2 class="font-serif text-heading leading-tight text-[var(--color-text-bright)]">Delete snapshot</h2>
			<p class="mt-1.5 text-ui text-[var(--color-text-tertiary)]">
				Permanently delete <code class="rounded bg-[var(--color-bg-4)] px-1.5 py-0.5 font-mono text-[0.8rem] text-[var(--color-text-primary)]">{deleteTarget.name}</code>.
				Running capsules won't be affected, but you won't be able to launch new ones from it.
			</p>

			{#if deleteTarget.type === 'snapshot'}
				<div class="mt-3 flex items-start gap-2 rounded-[var(--radius-input)] border border-[var(--color-amber)]/20 bg-[var(--color-amber)]/5 px-3 py-2.5">
					<svg class="mt-0.5 shrink-0" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="var(--color-amber)" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
						<path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z" />
						<line x1="12" y1="9" x2="12" y2="13" /><line x1="12" y1="17" x2="12.01" y2="17" />
					</svg>
					<p class="text-meta leading-relaxed text-[var(--color-amber)]">
						This snapshot includes memory state. Paused capsules that depend on it won't be able to resume.
					</p>
				</div>
			{/if}

			{#if deleteError}
				<div class="mt-3 rounded-[var(--radius-input)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-3 py-2 text-meta text-[var(--color-red)]">
					{deleteError}
				</div>
			{/if}

			<div class="mt-6 flex justify-end gap-3">
				<button
					onclick={() => (deleteTarget = null)}
					disabled={deleting}
					class="rounded-[var(--radius-button)] border border-[var(--color-border)] px-4 py-2 text-ui text-[var(--color-text-secondary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)] disabled:opacity-50"
				>
					Cancel
				</button>
				<button
					onclick={handleDelete}
					disabled={deleting}
					class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-red)] px-5 py-2 text-ui font-semibold text-white transition-all duration-150 hover:brightness-110 hover:-translate-y-px active:translate-y-0 disabled:opacity-50 disabled:hover:translate-y-0"
				>
					{#if deleting}
						<svg class="animate-spin" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
							<path d="M21 12a9 9 0 1 1-6.219-8.56" />
						</svg>
						Deleting...
					{:else}
						Delete Snapshot
					{/if}
				</button>
			</div>
			</div>
		</div>
	</div>
{/if}

<!-- Launch Dialog -->
{#if launchTarget}
	<div class="fixed inset-0 z-50 flex items-center justify-center">
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="absolute inset-0 bg-black/60"
			onclick={() => { if (!launching) launchTarget = null; }}
			onkeydown={(e) => { if (e.key === 'Escape' && !launching) launchTarget = null; }}
		></div>

		<div
			class="relative w-full max-w-[420px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)]"
			style="animation: fadeUp 0.2s ease both; box-shadow: var(--shadow-dialog)"
		>

			<div class="p-6">
			<h2 class="font-serif text-heading text-[var(--color-text-bright)]">Launch Capsule</h2>
			<p class="mt-1 text-ui text-[var(--color-text-tertiary)]">
				Configure resources and launch a new capsule from this template.
			</p>

			{#if launchError}
				<div class="mt-4 rounded-[var(--radius-input)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-3 py-2 text-meta text-[var(--color-red)]">
					{launchError}
				</div>
			{/if}

			<!-- Template name (readonly) -->
			<div class="mt-5">
				<label class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]">
					Template
				</label>
				<div class="flex items-center gap-2 rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-0)] px-3 py-2">
					{#if launchTarget.type === 'snapshot'}
						<span class="h-1.5 w-1.5 shrink-0 rounded-full bg-[var(--color-accent)]"></span>
					{:else}
						<span class="h-1.5 w-1.5 shrink-0 rounded-full bg-[var(--color-blue)]"></span>
					{/if}
					<span class="flex-1 font-mono text-ui text-[var(--color-text-bright)]">{launchTarget.name}</span>
					<span class="text-label text-[var(--color-text-muted)]">
						{launchTarget.type === 'snapshot' ? 'Snapshot' : 'Image'}
					</span>
				</div>
			</div>

			<!-- vCPUs + Memory -->
			<div class="mt-4 grid grid-cols-2 gap-3">
				<div>
					<label class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]" for="launch-vcpus">
						vCPUs
					</label>
					{#if launchTarget.type === 'snapshot'}
						<div class="rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-0)] px-3 py-2 font-mono text-ui text-[var(--color-text-muted)]">
							{launchTarget.vcpus ?? 1}
						</div>
					{:else}
						<input
							id="launch-vcpus"
							type="number"
							min="1"
							max="32"
							bind:value={launchVcpus}
							class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-4)] px-3 py-2 font-mono text-ui text-[var(--color-text-bright)] outline-none transition-colors duration-150 focus:border-[var(--color-accent)]"
						/>
					{/if}
				</div>

				<div>
					<label class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]" for="launch-memory">
						Memory (MB)
					</label>
					{#if launchTarget.type === 'snapshot'}
						<div class="rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-0)] px-3 py-2 font-mono text-ui text-[var(--color-text-muted)]">
							{launchTarget.memory_mb ?? 512}
						</div>
					{:else}
						<input
							id="launch-memory"
							type="number"
							min="128"
							step="128"
							bind:value={launchMemoryMb}
							class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-4)] px-3 py-2 font-mono text-ui text-[var(--color-text-bright)] outline-none transition-colors duration-150 focus:border-[var(--color-accent)]"
						/>
					{/if}
				</div>
			</div>

			<!-- Timeout -->
			<div class="mt-4">
				<label class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]" for="launch-timeout">Idle timeout</label>
				<input
					id="launch-timeout"
					type="number"
					min="0"
					bind:value={launchTimeoutSec}
					placeholder="0"
					class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-4)] px-3 py-2 font-mono text-ui text-[var(--color-text-bright)] outline-none transition-colors duration-150 focus:border-[var(--color-accent)]"
				/>
				<p class="mt-1.5 text-meta text-[var(--color-text-muted)]">Seconds of inactivity before the capsule pauses. Set to 0 to keep it running indefinitely.</p>
			</div>

			<div class="mt-6 flex justify-end gap-3">
				<button
					onclick={() => (launchTarget = null)}
					disabled={launching}
					class="rounded-[var(--radius-button)] border border-[var(--color-border)] px-4 py-2 text-ui text-[var(--color-text-secondary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)] disabled:opacity-50"
				>
					Cancel
				</button>
				<button
					onclick={handleLaunch}
					disabled={launching}
					class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-accent)] px-5 py-2 text-ui font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0 disabled:opacity-50 disabled:hover:translate-y-0"
				>
					{#if launching}
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
	</div>
{/if}

<style>
	/* Skeleton shimmer — GPU-composited, no paint cost */
	.skeleton {
		background: linear-gradient(
			90deg,
			var(--color-bg-3) 25%,
			var(--color-bg-4) 50%,
			var(--color-bg-3) 75%
		);
		background-size: 200% 100%;
		animation: shimmer 1.4s ease infinite;
	}

	@keyframes shimmer {
		0% { background-position: -200% 0; }
		100% { background-position: 200% 0; }
	}

	/* Left accent stripe — slides in on hover, color-keyed to snapshot type */
	.row-stripe {
		transform: scaleY(0);
		transform-origin: center;
		transition: transform 0.18s cubic-bezier(0.25, 1, 0.5, 1);
	}
	.snapshot-row:hover .row-stripe {
		transform: scaleY(1);
	}

	/* Type-tinted row hover backgrounds */
	.snapshot-row.type-snapshot:hover {
		background: rgba(94, 140, 88, 0.04);
	}
	.snapshot-row.type-image:hover {
		background: rgba(90, 159, 212, 0.04);
	}

	/* Empty state icon float — matches admin pattern */
	.empty-icon-float {
		animation: iconFloat 3s ease-in-out infinite;
	}
</style>
