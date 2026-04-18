<script lang="ts">
	import { onMount } from 'svelte';
	import { listAuditLogs, type AuditLog } from '$lib/api/audit';

	// ─── Data state ───────────────────────────────────────────────────────────

	let logs = $state<AuditLog[]>([]);
	let loading = $state(true);
	let loadingMore = $state(false);
	let error = $state<string | null>(null);
	let hasMore = $state(false);
	let nextCursor = $state<{ before: string; before_id: string } | null>(null);

	// ─── UI state ─────────────────────────────────────────────────────────────

	let sentinel = $state<HTMLElement | null>(null);
	let filterDropdownOpen = $state(false);
	let filterDropdownEl = $state<HTMLElement | null>(null);

	// ─── Filter state ─────────────────────────────────────────────────────────
	// Map: resource_type → Set of selected actions for that resource.
	// An empty set or absent key means no filter for that resource.

	let selectedActions = $state<Map<string, Set<string>>>(new Map());

	// ─── Constants ────────────────────────────────────────────────────────────

	const RESOURCES = ['sandbox', 'snapshot', 'team', 'api_key', 'member', 'host'] as const;

	const RESOURCE_LABELS: Record<string, string> = {
		sandbox: 'Capsule',
		snapshot: 'Template',
		team: 'Team',
		api_key: 'API Key',
		member: 'Member',
		host: 'Host'
	};

	const ACTIONS_BY_RESOURCE: Record<string, string[]> = {
		sandbox: ['create', 'pause', 'resume', 'destroy'],
		snapshot: ['create', 'delete'],
		team: ['rename'],
		api_key: ['create', 'revoke'],
		member: ['add', 'remove', 'leave', 'role_update'],
		host: ['create', 'delete', 'marked_down', 'marked_up']
	};

	const ACTION_LABELS: Record<string, string> = {
		create: 'Created',
		pause: 'Paused',
		resume: 'Resumed',
		destroy: 'Destroyed',
		delete: 'Deleted',
		rename: 'Renamed',
		revoke: 'Revoked',
		add: 'Added',
		remove: 'Removed',
		leave: 'Left',
		role_update: 'Role updated',
		marked_down: 'Marked down',
		marked_up: 'Marked up'
	};

	// ─── Derived ──────────────────────────────────────────────────────────────

	let activeFilterCount = $derived(
		[...selectedActions.values()].filter((s) => s.size > 0).length
	);

	// ─── Filter helpers ───────────────────────────────────────────────────────

	type CheckState = 'all' | 'some' | 'none';

	function getResourceCheckState(r: string): CheckState {
		const sel = selectedActions.get(r);
		if (!sel || sel.size === 0) return 'none';
		if (sel.size === ACTIONS_BY_RESOURCE[r].length) return 'all';
		return 'some';
	}

	function toggleResource(r: string) {
		const state = getResourceCheckState(r);
		const next = new Map(selectedActions);
		if (state === 'all') {
			next.delete(r);
		} else {
			next.set(r, new Set(ACTIONS_BY_RESOURCE[r]));
		}
		selectedActions = next;
		resetAndFetch(next);
	}

	function toggleAction(r: string, a: string) {
		const next = new Map(selectedActions);
		const acts = new Set(next.get(r) ?? []);
		if (acts.has(a)) {
			acts.delete(a);
		} else {
			acts.add(a);
		}
		if (acts.size === 0) {
			next.delete(r);
		} else {
			next.set(r, acts);
		}
		selectedActions = next;
		resetAndFetch(next);
	}

	function clearAllFilters() {
		selectedActions = new Map();
		resetAndFetch(new Map());
	}

	function getApiParams(snap: Map<string, Set<string>>) {
		const resource_types: string[] = [];
		const actions = new Set<string>();
		for (const [r, acts] of snap) {
			if (acts.size > 0) {
				resource_types.push(r);
				acts.forEach((a) => actions.add(a));
			}
		}
		return {
			resource_types: resource_types.length > 0 ? resource_types : undefined,
			actions: actions.size > 0 ? [...actions] : undefined
		};
	}

	// ─── Click-outside to close dropdown ─────────────────────────────────────

	$effect(() => {
		if (!filterDropdownOpen) return;
		function handleMouseDown(e: MouseEvent) {
			if (filterDropdownEl && !filterDropdownEl.contains(e.target as Node)) {
				filterDropdownOpen = false;
			}
		}
		document.addEventListener('mousedown', handleMouseDown);
		return () => document.removeEventListener('mousedown', handleMouseDown);
	});

	// ─── Data functions ───────────────────────────────────────────────────────

	let fetchId = 0;

	async function resetAndFetch(snap: Map<string, Set<string>>) {
		const id = ++fetchId;
		loading = true;
		error = null;
		logs = [];
		nextCursor = null;
		hasMore = false;

		const params = getApiParams(snap);
		const result = await listAuditLogs(params);

		if (id !== fetchId) return;

		if (result.ok) {
			logs = result.data.items;
			hasMore = !!result.data.next_before;
			nextCursor = result.data.next_before
				? { before: result.data.next_before, before_id: result.data.next_before_id! }
				: null;
		} else {
			error = result.error;
		}
		loading = false;
	}

	async function loadNextPage() {
		if (!nextCursor || loadingMore) return;
		loadingMore = true;

		const params = getApiParams(selectedActions);
		const result = await listAuditLogs({
			...params,
			before: nextCursor.before,
			before_id: nextCursor.before_id
		});

		if (result.ok) {
			logs = [...logs, ...result.data.items];
			hasMore = !!result.data.next_before;
			nextCursor = result.data.next_before
				? { before: result.data.next_before, before_id: result.data.next_before_id! }
				: null;
		}
		loadingMore = false;
	}

	// ─── UI helpers ───────────────────────────────────────────────────────────

	function describeEvent(log: AuditLog): string {
		const actor = log.actor_name || (log.actor_type === 'system' ? 'System' : 'Unknown');
		const meta = (log.metadata ?? {}) as Record<string, string>;
		switch (`${log.resource_type}:${log.action}`) {
			case 'sandbox:create':     return `${actor} created a capsule`;
			case 'sandbox:pause':      return `${actor} paused a capsule`;
			case 'sandbox:resume':     return `${actor} resumed a capsule`;
			case 'sandbox:destroy':    return `${actor} destroyed a capsule`;
			case 'snapshot:create':    return `${actor} created a template`;
			case 'snapshot:delete':    return `${actor} deleted a template`;
			case 'team:rename':        return `${actor} renamed the team from "${meta.old_name}" to "${meta.new_name}"`;
			case 'api_key:create':     return `${actor} created API key "${meta.name}"`;
			case 'api_key:revoke':     return `${actor} revoked an API key`;
			case 'member:add':         return `${actor} added ${meta.email} as ${meta.role}`;
			case 'member:remove':      return `${actor} removed ${meta.email ?? 'a member'}`;
			case 'member:leave':       return `${actor} left the team`;
			case 'member:role_update': return `${actor} changed a member's role to ${meta.new_role}`;
			case 'host:create':        return `${actor} registered a host`;
			case 'host:delete':        return `${actor} removed a host`;
			case 'host:marked_down':   return `Host was marked as down`;
			case 'host:marked_up':     return `Host was marked as up`;
			default:                   return `${actor} performed ${log.action} on ${log.resource_type}`;
		}
	}

	function actorLabel(log: AuditLog): string {
		if (log.actor_type === 'system') return 'System';
		return log.actor_name ?? '—';
	}

	function formatEventDate(iso: string): { date: string; time: string } {
		const d = new Date(iso);
		return {
			date: d.toLocaleString('en-US', { month: 'short', day: 'numeric', year: 'numeric' }),
			time: d.toLocaleString('en-US', { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false })
		};
	}

	function statusColor(status: string): string {
		switch (status) {
			case 'success': return 'var(--color-accent)';
			case 'info':    return 'var(--color-blue)';
			case 'warning': return 'var(--color-amber)';
			case 'error':   return 'var(--color-red)';
			default:        return 'var(--color-text-muted)';
		}
	}

	function tagLabel(r: string): string {
		const sel = selectedActions.get(r);
		if (!sel || sel.size === 0) return RESOURCE_LABELS[r];
		const total = ACTIONS_BY_RESOURCE[r].length;
		if (sel.size === total) return RESOURCE_LABELS[r];
		const actionNames = [...sel].map((a) => ACTION_LABELS[a]).join(', ');
		return `${RESOURCE_LABELS[r]}: ${actionNames}`;
	}

	// ─── Lifecycle ────────────────────────────────────────────────────────────

	onMount(() => {
		resetAndFetch(new Map());
	});

	$effect(() => {
		const el = sentinel;
		if (!el) return;
		const obs = new IntersectionObserver(
			([entry]) => {
				if (entry.isIntersecting && !loadingMore && !loading && hasMore) {
					loadNextPage();
				}
			},
			{ rootMargin: '300px' }
		);
		obs.observe(el);
		return () => obs.disconnect();
	});
</script>

<svelte:head>
	<title>Wrenn — Audit Logs</title>
</svelte:head>

<main class="flex-1 overflow-y-auto bg-[var(--color-bg-0)]">

			<!-- Header -->
			<div class="px-7 pt-8">
				<h1 class="font-serif text-page text-[var(--color-text-bright)]">
					Audit Logs
				</h1>
				<p class="mt-2 text-ui text-[var(--color-text-secondary)]">
					A complete record of activity across your team.
				</p>
				<div class="mt-6 border-b border-[var(--color-border)]"></div>
			</div>

			<!-- Filter bar -->
			<div class="px-7 pt-5">
				<div class="flex items-center gap-2">

					<!-- Single hierarchical filter dropdown -->
					<div class="relative" bind:this={filterDropdownEl}>
						<button
							onclick={() => (filterDropdownOpen = !filterDropdownOpen)}
							class="flex items-center gap-2 rounded-[var(--radius-button)] border px-3 py-1.5 text-ui transition-colors duration-150
								{activeFilterCount > 0
									? 'border-[var(--color-accent)]/60 bg-[var(--color-accent)]/10 font-medium text-[var(--color-accent)]'
									: 'border-[var(--color-border)] bg-[var(--color-bg-3)] text-[var(--color-text-secondary)] hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)]'}"
						>
							<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
								<line x1="4" y1="6" x2="20" y2="6" />
								<line x1="8" y1="12" x2="16" y2="12" />
								<line x1="11" y1="18" x2="13" y2="18" />
							</svg>
							<span>Filter</span>
							{#if activeFilterCount > 0}
								<span class="flex h-4 w-4 items-center justify-center rounded-full bg-[var(--color-accent)] text-[10px] font-semibold leading-none text-white">
									{activeFilterCount}
								</span>
							{/if}
							<svg
								class="transition-transform duration-150 {filterDropdownOpen ? 'rotate-180' : ''}"
								width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"
							>
								<polyline points="6 9 12 15 18 9" />
							</svg>
						</button>

						{#if filterDropdownOpen}
							<div
								class="absolute left-0 top-full z-20 mt-1.5 w-56 overflow-y-auto rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] py-1.5 shadow-xl"
								style="max-height: 380px; animation: fadeUp 0.12s ease both"
							>
								{#each RESOURCES as r}
									{@const rState = getResourceCheckState(r)}
									{@const actions = ACTIONS_BY_RESOURCE[r]}

									<!-- Resource row -->
									<label class="flex cursor-pointer items-center gap-2.5 px-3 py-2 transition-colors duration-100 hover:bg-[var(--color-bg-3)]">
										<!-- Tristate checkbox -->
										<span class="flex h-3.5 w-3.5 shrink-0 items-center justify-center rounded-sm border transition-colors duration-100
											{rState !== 'none' ? 'border-[var(--color-accent)] bg-[var(--color-accent)]' : 'border-[var(--color-border-mid)] bg-[var(--color-bg-4)]'}">
											{#if rState === 'all'}
												<svg width="8" height="8" viewBox="0 0 24 24" fill="none" stroke="white" stroke-width="3.5" stroke-linecap="round" stroke-linejoin="round">
													<polyline points="20 6 9 17 4 12" />
												</svg>
											{:else if rState === 'some'}
												<svg width="8" height="8" viewBox="0 0 24 24" fill="none" stroke="white" stroke-width="3" stroke-linecap="round">
													<line x1="5" y1="12" x2="19" y2="12" />
												</svg>
											{/if}
										</span>
										<input type="checkbox" class="sr-only" checked={rState !== 'none'} onchange={() => toggleResource(r)} />
										<span class="text-ui font-medium text-[var(--color-text-primary)]">{RESOURCE_LABELS[r]}</span>
									</label>

									<!-- Action rows (indented) -->
									{#each actions as a}
										{@const checked = selectedActions.get(r)?.has(a) ?? false}
										<label class="flex cursor-pointer items-center gap-2.5 py-1.5 pl-8 pr-3 transition-colors duration-100 hover:bg-[var(--color-bg-3)]">
											<span class="flex h-3.5 w-3.5 shrink-0 items-center justify-center rounded-sm border transition-colors duration-100
												{checked ? 'border-[var(--color-accent)] bg-[var(--color-accent)]' : 'border-[var(--color-border-mid)] bg-[var(--color-bg-4)]'}">
												{#if checked}
													<svg width="8" height="8" viewBox="0 0 24 24" fill="none" stroke="white" stroke-width="3.5" stroke-linecap="round" stroke-linejoin="round">
														<polyline points="20 6 9 17 4 12" />
													</svg>
												{/if}
											</span>
											<input type="checkbox" class="sr-only" {checked} onchange={() => toggleAction(r, a)} />
											<span class="text-ui text-[var(--color-text-secondary)]">{ACTION_LABELS[a]}</span>
										</label>
									{/each}

									<!-- Divider between resource groups -->
									{#if r !== RESOURCES[RESOURCES.length - 1]}
										<div class="mx-3 my-1 border-t border-[var(--color-border)]"></div>
									{/if}
								{/each}
							</div>
						{/if}
					</div>

				</div>

				<!-- Active filter tags -->
				{#if activeFilterCount > 0}
					<div class="mt-3 flex flex-wrap items-center gap-2" style="animation: fadeUp 0.2s ease both">
						{#each RESOURCES as r}
							{#if (selectedActions.get(r)?.size ?? 0) > 0}
								<span class="flex items-center gap-1.5 rounded-full border border-[var(--color-accent)]/40 bg-[var(--color-accent)]/10 px-2.5 py-1 text-meta font-medium text-[var(--color-accent)]">
									{tagLabel(r)}
									<button
										onclick={() => toggleResource(r)}
										class="flex items-center justify-center text-[var(--color-accent)] opacity-60 transition-opacity duration-100 hover:opacity-100"
										aria-label="Remove {RESOURCE_LABELS[r]} filter"
									>
										<svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round">
											<line x1="18" y1="6" x2="6" y2="18" /><line x1="6" y1="6" x2="18" y2="18" />
										</svg>
									</button>
								</span>
							{/if}
						{/each}
						<button
							onclick={clearAllFilters}
							class="text-meta text-[var(--color-text-muted)] underline-offset-2 transition-colors duration-100 hover:text-[var(--color-text-secondary)] hover:underline"
						>
							Clear all
						</button>
					</div>
				{/if}
			</div>

			<!-- Content -->
			<div class="p-8" style="animation: fadeUp 0.35s ease both">

				{#if error}
					<div class="mb-4 flex items-center justify-between gap-4 rounded-[var(--radius-card)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-4 py-3 text-ui text-[var(--color-red)]">
						<span>{error}</span>
						<button
							onclick={() => resetAndFetch(selectedActions)}
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
							Loading events...
						</div>
					</div>
				{:else if logs.length === 0}
					<!-- Empty state -->
					<div class="flex flex-col items-center justify-center py-[72px]">
						<div class="relative mb-5">
							<div class="absolute inset-0 -m-4 rounded-full" style="background: radial-gradient(circle, rgba(94,140,88,0.08) 0%, transparent 70%)"></div>
							<div
								class="relative flex h-14 w-14 items-center justify-center rounded-[var(--radius-card)] border border-[var(--color-accent)]/20 bg-[var(--color-bg-3)]"
								style="animation: iconFloat 4s ease-in-out infinite"
							>
								<svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="var(--color-accent-mid)" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
									<path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z" />
								</svg>
							</div>
						</div>
						<p class="font-serif text-heading text-[var(--color-text-bright)]">
							{activeFilterCount > 0 ? 'No matching events' : 'No activity yet'}
						</p>
						<p class="mt-1.5 text-ui text-[var(--color-text-tertiary)]">
							{activeFilterCount > 0
								? 'Try adjusting or clearing the filters.'
								: 'Events will appear here as your team takes actions.'}
						</p>
						{#if activeFilterCount > 0}
							<button
								onclick={clearAllFilters}
								class="mt-4 text-ui text-[var(--color-accent)] underline-offset-2 hover:underline"
							>
								Clear filters
							</button>
						{/if}
					</div>
				{:else}
					<!-- Table -->
					<div class="overflow-hidden rounded-[var(--radius-card)] border border-[var(--color-border)]">

						<!-- Table header -->
						<div class="grid grid-cols-[168px_1.4fr_3fr] border-b border-[var(--color-border)] bg-[var(--color-bg-3)]">
							<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Time</div>
							<div class="px-4 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Actor</div>
							<div class="px-4 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Event</div>
						</div>

						<!-- Rows -->
						{#each logs as log, i (log.id)}
							{@const ts = formatEventDate(log.created_at)}
							<div
								class="log-entry relative overflow-hidden border-b border-[var(--color-border)] last:border-b-0
									{log.status === 'error' ? 'log-row-error' : ''}
									{log.status === 'warning' ? 'log-row-warning' : ''}"
								style="animation: fadeUp 0.35s ease both; animation-delay: {Math.min(i, 10) * 30}ms"
							>
								<!-- Status stripe (absolutely positioned, independent of row animation) -->
								<div
									class="status-stripe pointer-events-none absolute inset-y-0 left-0 w-[3px] {log.status === 'error' ? 'stripe-pulse' : ''}"
									style="background: {statusColor(log.status)}"
								></div>

								<!-- Main row -->
								<div class="grid grid-cols-[168px_1.4fr_3fr] items-start">
									<!-- Time -->
									<div class="flex flex-col gap-0.5 px-5 py-4">
										<span class="text-ui text-[var(--color-text-secondary)]">{ts.date}</span>
										<span class="font-mono text-meta text-[var(--color-text-muted)]">{ts.time}</span>
									</div>

									<!-- Actor -->
									<div class="min-w-0 px-4 py-4">
										<div class="flex flex-col gap-1">
											<span class="truncate text-ui font-medium text-[var(--color-text-bright)]">
												{actorLabel(log)}
											</span>
											{#if log.actor_type === 'api_key'}
												<span class="inline-flex w-fit items-center rounded-sm border border-[var(--color-border-mid)] bg-[var(--color-bg-4)] px-1.5 py-0.5 font-mono text-badge text-[var(--color-text-muted)]">key</span>
											{/if}
										</div>
									</div>

									<!-- Event description + resource ID -->
									<div class="min-w-0 px-4 py-4">
										<p class="text-ui font-medium text-[var(--color-text-primary)]">{describeEvent(log)}</p>
										{#if log.resource_id}
											<span class="mt-1 inline-flex items-center rounded-sm border border-[var(--color-border-mid)] bg-[var(--color-bg-4)] px-1.5 py-0.5 font-mono text-badge text-[var(--color-text-muted)]">{log.resource_id}</span>
										{/if}
									</div>
								</div>
							</div>
						{/each}
					</div>

					<!-- Load more sentinel + status -->
					<div bind:this={sentinel} class="mt-4">
						{#if loadingMore}
							<div class="flex items-center justify-center gap-2 py-6 text-meta text-[var(--color-text-muted)]">
								<svg class="animate-spin" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
									<path d="M21 12a9 9 0 1 1-6.219-8.56" />
								</svg>
								Loading more...
							</div>
						{:else if !hasMore}
							<p class="py-4 text-center text-meta text-[var(--color-text-muted)]">
								{logs.length} {logs.length === 1 ? 'event' : 'events'} total
							</p>
						{/if}
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
	/* fadeUp and iconFloat are defined globally in app.css — no need to redeclare them here */

	@keyframes stripePulse {
		0%, 100% { opacity: 1; }
		50%       { opacity: 0.3; }
	}

	.log-row-error {
		background: rgba(207, 129, 114, 0.04);
	}

	.log-row-warning {
		background: rgba(212, 167, 60, 0.03);
	}

	.stripe-pulse {
		animation: stripePulse 2.5s ease-in-out infinite;
	}
</style>
