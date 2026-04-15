<script lang="ts">
	import { onMount } from 'svelte';
	import { toast } from '$lib/toast.svelte';
	import { formatDate, timeAgo } from '$lib/utils/format';
	import {
		listHosts,
		createHost,
		deleteHost,
		getDeletePreview,
		statusColor,
		formatSpecs,
		type Host,
		type CreateHostResult
	} from '$lib/api/hosts';

	const PAGE_SIZE = 50;

	let activeTab = $state<'platform' | 'byoc'>('platform');

	// All hosts fetched once
	let allHosts = $state<Host[]>([]);
	let loading = $state(true);
	let error = $state<string | null>(null);

	// Platform tab state
	let platformHosts = $derived(allHosts.filter((h) => h.type === 'regular'));

	// BYOC tab state — grouped by team, sorted by count descending, paginated
	let byocHosts = $derived(allHosts.filter((h) => h.type === 'byoc'));
	let byocPage = $state(0);

	type TeamGroup = { teamId: string | null; teamName: string; hosts: Host[] };

	let byocGroups = $derived.by<TeamGroup[]>(() => {
		const map = new Map<string, TeamGroup>();
		for (const h of byocHosts) {
			const key = h.team_id ?? '__none__';
			if (!map.has(key)) {
				map.set(key, {
					teamId: h.team_id ?? null,
					teamName: h.team_name ?? h.team_id ?? 'Unknown Team',
					hosts: []
				});
			}
			map.get(key)!.hosts.push(h);
		}
		return [...map.values()].sort((a, b) => b.hosts.length - a.hosts.length);
	});

	// Flatten for pagination: all byoc hosts sorted by team group count order
	let flatByocHosts = $derived(byocGroups.flatMap((g) => g.hosts));
	let byocPageCount = $derived(Math.max(1, Math.ceil(flatByocHosts.length / PAGE_SIZE)));
	let byocPageHosts = $derived(flatByocHosts.slice(byocPage * PAGE_SIZE, (byocPage + 1) * PAGE_SIZE));

	// Stats across all hosts
	let onlineCount = $derived(allHosts.filter((h) => h.status === 'online').length);
	let pendingCount = $derived(allHosts.filter((h) => h.status === 'pending').length);
	let totalCount = $derived(allHosts.length);

	// Create dialog (platform hosts)
	let showCreate = $state(false);
	let createForm = $state({ provider: '', availability_zone: '' });
	let creating = $state(false);
	let createError = $state<string | null>(null);

	// Token reveal
	let createdResult = $state<CreateHostResult | null>(null);
	let tokenCopied = $state(false);
	let checkmarkVisible = $state(false);

	// Delete confirmation
	let deleteTarget = $state<Host | null>(null);
	let deletePreviewCapsules = $state<string[]>([]);
	let deletePreviewLoading = $state(false);
	let deleting = $state(false);
	let deleteError = $state<string | null>(null);

	let flashHostId = $state<string | null>(null);
	let newHostId = $state<string | null>(null);

	async function fetchHosts() {
		loading = true;
		error = null;
		const result = await listHosts();
		if (result.ok) {
			allHosts = result.data;
		} else {
			error = result.error;
		}
		loading = false;
	}

	async function handleCreatePlatform() {
		creating = true;
		createError = null;
		const result = await createHost({
			type: 'regular',
			provider: createForm.provider.trim() || undefined,
			availability_zone: createForm.availability_zone.trim() || undefined
		});
		if (result.ok) {
			showCreate = false;
			createForm = { provider: '', availability_zone: '' };
			createdResult = result.data;
			newHostId = result.data.host.id;
			allHosts = [result.data.host, ...allHosts];
			flashHostId = result.data.host.id;
			// Trigger checkmark animation after modal mounts
			setTimeout(() => (checkmarkVisible = true), 80);
			setTimeout(() => (flashHostId = null), 2500);
		} else {
			createError = result.error;
		}
		creating = false;
	}

	async function openDeleteConfirm(host: Host) {
		deleteTarget = host;
		deleteError = null;
		deletePreviewCapsules = [];
		deletePreviewLoading = true;
		const preview = await getDeletePreview(host.id);
		deletePreviewLoading = false;
		if (preview.ok) {
			deletePreviewCapsules = preview.data.sandbox_ids;
		}
	}

	async function handleDelete() {
		if (!deleteTarget) return;
		deleting = true;
		deleteError = null;
		const result = await deleteHost(deleteTarget.id, deletePreviewCapsules.length > 0);
		if (result.ok) {
			allHosts = allHosts.filter((h) => h.id !== deleteTarget!.id);
			deleteTarget = null;
			toast.success('Host deleted');
		} else {
			deleteError = result.error;
		}
		deleting = false;
	}

	async function copyToken(token: string) {
		await navigator.clipboard.writeText(token);
		tokenCopied = true;
		setTimeout(() => (tokenCopied = false), 2000);
	}

	function closeTokenReveal() {
		createdResult = null;
		checkmarkVisible = false;
		newHostId = null;
	}

	onMount(fetchHosts);
</script>

<main class="flex min-w-0 flex-1 flex-col overflow-hidden">
	<!-- Header -->
	<header class="relative shrink-0 border-b border-[var(--color-border)] bg-[var(--color-bg-1)]">
		<!-- Subtle gradient wash behind header for depth -->
		<div class="absolute inset-0 bg-gradient-to-b from-[var(--color-accent)]/[0.02] to-transparent pointer-events-none"></div>

		<div class="relative flex items-start justify-between px-8 pt-7 pb-5">
			<div>
				<h1 class="font-serif text-page leading-none text-[var(--color-text-bright)]">
					Hosts
					</h1>
					<p class="mt-2 text-ui text-[var(--color-text-tertiary)]">
						Platform and BYOC compute across all teams.
					</p>
				</div>
				{#if activeTab === 'platform'}
					<button
						onclick={() => { showCreate = true; createError = null; createForm = { provider: '', availability_zone: '' }; }}
						class="group flex items-center gap-2.5 rounded-[var(--radius-button)] bg-[var(--color-accent)] px-5 py-2.5 text-ui font-semibold text-white shadow-sm transition-all duration-200 hover:shadow-[0_0_20px_var(--color-accent-glow-mid)] hover:brightness-115 hover:-translate-y-px active:translate-y-0"
					>
						<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" class="transition-transform duration-200 group-hover:rotate-90"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>
						Add Host
					</button>
				{/if}
			</div>

			<!-- Stat strip — bolder presence -->
			{#if !loading && !error}
				<div class="relative flex items-center gap-3 px-8 pb-5">
					<div class="stat-pill border-[var(--color-border)] bg-[var(--color-bg-2)]">
						<span class="font-mono text-body font-bold tabular-nums text-[var(--color-text-bright)]">{totalCount}</span>
						<span class="text-label text-[var(--color-text-muted)]">total</span>
					</div>
					<div class="stat-pill border-[var(--color-accent)]/25 bg-[var(--color-accent)]/8 gap-2">
						<span class="relative flex h-2 w-2 shrink-0">
							<span class="animate-status-ping absolute inline-flex h-full w-full rounded-full bg-[var(--color-accent)] opacity-60"></span>
							<span class="relative inline-flex h-2 w-2 rounded-full bg-[var(--color-accent)]"></span>
						</span>
						<span class="font-mono text-body font-bold tabular-nums text-[var(--color-accent-bright)]">{onlineCount}</span>
						<span class="text-label text-[var(--color-accent-bright)]/70">online</span>
					</div>
					{#if pendingCount > 0}
						<div class="stat-pill border-[var(--color-amber)]/25 bg-[var(--color-amber)]/8">
							<span class="font-mono text-body font-bold tabular-nums text-[var(--color-amber)]">{pendingCount}</span>
							<span class="text-label text-[var(--color-amber)]/70">pending</span>
						</div>
					{/if}
				</div>
			{/if}
		</header>

		<!-- Tabs — heavier presence -->
		<div class="flex shrink-0 border-b border-[var(--color-border)] bg-[var(--color-bg-1)] px-8">
			{#each [['platform', 'Platform', platformHosts.length], ['byoc', 'BYOC', byocHosts.length]] as [id, label, count] (id)}
				<button
					onclick={() => { activeTab = id as 'platform' | 'byoc'; if (id === 'byoc') byocPage = 0; }}
					class="tab-button {activeTab === id
						? 'text-[var(--color-text-bright)] font-medium'
						: 'text-[var(--color-text-tertiary)] hover:text-[var(--color-text-secondary)]'}"
				>
					{label}
					{#if !loading}
						<span class="ml-2 rounded-full px-2 py-0.5 text-label tabular-nums transition-colors duration-200 {activeTab === id
							? 'bg-[var(--color-accent)]/12 text-[var(--color-accent-bright)]'
							: 'bg-[var(--color-bg-4)] text-[var(--color-text-muted)]'}">
							{count}
						</span>
					{/if}
					{#if activeTab === id}
						<span class="absolute bottom-0 left-0 right-0 h-[2px] rounded-t-full bg-[var(--color-accent)]"></span>
					{/if}
				</button>
			{/each}
		</div>

		<!-- Body -->
		<div class="flex-1 overflow-y-auto px-8 py-6">
			{#if loading}
				{@render skeletonRows()}
			{:else if error}
				<div class="rounded-[var(--radius-card)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-4 py-3 text-ui text-[var(--color-red)]">
					{error}
				</div>
			{:else if activeTab === 'platform'}
				{@render hostsTable(platformHosts, false)}
			{:else}
				<!-- BYOC hosts: grouped by team -->
				{#if byocHosts.length === 0}
					{@render emptyState('byoc')}
				{:else}
					<div class="space-y-6">
						{#each byocGroups as group (group.teamId ?? '__none__')}
							{@const groupPageHosts = byocPageHosts.filter(h => h.team_id === group.teamId || (group.teamId === null && !h.team_id))}
							{#if groupPageHosts.length > 0}
								<div>
									<div class="mb-3 flex items-center gap-2.5">
										<span class="text-label font-semibold uppercase tracking-[0.08em] text-[var(--color-text-tertiary)]">
											{group.teamName}
										</span>
										<span class="rounded-full bg-[var(--color-bg-3)] px-2 py-0.5 font-mono text-label tabular-nums text-[var(--color-text-muted)]">
											{group.hosts.length}
										</span>
									</div>
									{@render hostsTable(groupPageHosts, false)}
								</div>
							{/if}
						{/each}

						<!-- Pagination -->
						{#if byocPageCount > 1}
							<div class="flex items-center justify-between pt-3">
								<span class="text-meta text-[var(--color-text-muted)]">
									Page <span class="font-mono tabular-nums">{byocPage + 1}</span> of <span class="font-mono tabular-nums">{byocPageCount}</span> · <span class="font-mono tabular-nums">{byocHosts.length}</span> hosts
								</span>
								<div class="flex items-center gap-2">
									<button
										onclick={() => (byocPage = Math.max(0, byocPage - 1))}
										disabled={byocPage === 0}
										class="rounded-[var(--radius-button)] border border-[var(--color-border)] px-3.5 py-1.5 text-meta text-[var(--color-text-secondary)] transition-all duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)] disabled:cursor-not-allowed disabled:opacity-40"
									>
										← Previous
									</button>
									<button
										onclick={() => (byocPage = Math.min(byocPageCount - 1, byocPage + 1))}
										disabled={byocPage >= byocPageCount - 1}
										class="rounded-[var(--radius-button)] border border-[var(--color-border)] px-3.5 py-1.5 text-meta text-[var(--color-text-secondary)] transition-all duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)] disabled:cursor-not-allowed disabled:opacity-40"
									>
										Next →
									</button>
								</div>
							</div>
						{/if}
					</div>
				{/if}
			{/if}
		</div>
	</main>

{#snippet skeletonRows()}
	<div class="rounded-[var(--radius-card)] border border-[var(--color-border)] bg-[var(--color-bg-1)] overflow-hidden shadow-sm">
		<table class="w-full">
			<thead>
				<tr class="border-b border-[var(--color-border)] bg-[var(--color-bg-0)]/40">
					<th class="table-header">Host</th>
					<th class="table-header">Status</th>
					<th class="table-header hidden md:table-cell">Specs</th>
					<th class="table-header hidden lg:table-cell">Last Heartbeat</th>
					<th class="table-header w-20"></th>
				</tr>
			</thead>
			<tbody>
				{#each Array(5) as _, i}
					<tr class="table-row-animate border-b border-[var(--color-border)] last:border-0" style="animation-delay: {i * 60}ms">
						<td class="px-5 py-3.5">
							<div class="skeleton mb-1.5 h-3 w-28 rounded"></div>
							<div class="skeleton h-2.5 w-20 rounded"></div>
						</td>
						<td class="px-5 py-3.5">
							<div class="skeleton h-3 w-16 rounded-full"></div>
						</td>
						<td class="hidden px-5 py-3.5 md:table-cell">
							<div class="skeleton h-3 w-24 rounded"></div>
						</td>
						<td class="hidden px-5 py-3.5 lg:table-cell">
							<div class="skeleton h-3 w-20 rounded"></div>
						</td>
						<td class="px-5 py-3.5 text-right">
							<div class="skeleton ml-auto h-6 w-14 rounded"></div>
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
	</div>
{/snippet}

{#snippet hostsTable(hosts: Host[], _showTeam: boolean)}
	{#if hosts.length === 0}
		{@render emptyState('platform')}
	{:else}
		<div class="rounded-[var(--radius-card)] border border-[var(--color-border)] bg-[var(--color-bg-1)] overflow-hidden shadow-sm">
			<table class="w-full">
				<thead>
					<tr class="border-b border-[var(--color-border)] bg-[var(--color-bg-0)]/40">
						<th class="table-header">Host</th>
						<th class="table-header">Status</th>
						<th class="table-header hidden md:table-cell">Specs</th>
						<th class="table-header hidden lg:table-cell">Last Heartbeat</th>
						<th class="table-header w-20"></th>
					</tr>
				</thead>
				<tbody>
					{#each hosts as host, i (host.id)}
						<tr
							class="table-row-animate border-b border-[var(--color-border)] last:border-0 transition-colors duration-200
								{host.id === newHostId ? 'new-row' : ''}
								{flashHostId === host.id ? 'bg-[var(--color-accent-glow)]' : 'hover:bg-[var(--color-bg-2)]'}
								{host.status === 'online' ? 'host-row-online' : ''}"
							style="animation-delay: {i * 30}ms"
						>
							<td class="px-5 py-3.5">
								<div class="font-mono text-ui font-medium text-[var(--color-text-bright)]">{host.id}</div>
								{#if host.address}
									<div class="mt-0.5 font-mono text-label text-[var(--color-text-muted)]">{host.address}</div>
								{/if}
								{#if host.provider || host.availability_zone}
									<div class="mt-0.5 text-label text-[var(--color-text-tertiary)]">
										{[host.provider, host.availability_zone].filter(Boolean).join(' · ')}
									</div>
								{/if}
							</td>
							<td class="px-5 py-3.5">
								<span class="flex items-center gap-2 text-meta font-semibold" style="color: {statusColor(host.status)}">
									{#if host.status === 'online'}
										<span class="relative flex h-2 w-2 shrink-0">
											<span class="animate-status-ping absolute inline-flex h-full w-full rounded-full opacity-60" style="background: {statusColor(host.status)}"></span>
											<span class="relative inline-flex h-2 w-2 rounded-full" style="background: {statusColor(host.status)}"></span>
										</span>
									{:else}
										<span class="h-2 w-2 shrink-0 rounded-full" style="background: {statusColor(host.status)}"></span>
									{/if}
									{host.status}
								</span>
							</td>
							<td class="hidden px-5 py-3.5 md:table-cell">
								<span class="font-mono text-meta tabular-nums text-[var(--color-text-secondary)]">{formatSpecs(host)}</span>
							</td>
							<td class="hidden px-5 py-3.5 lg:table-cell">
								<span class="text-meta text-[var(--color-text-muted)]" title={host.last_heartbeat_at ? formatDate(host.last_heartbeat_at) : undefined}>
									{host.last_heartbeat_at ? timeAgo(host.last_heartbeat_at) : '—'}
								</span>
							</td>
							<td class="px-5 py-3.5 text-right">
								<button
									onclick={() => openDeleteConfirm(host)}
									class="rounded-[var(--radius-button)] px-3 py-1.5 text-meta text-[var(--color-text-tertiary)] transition-all duration-150 hover:bg-[var(--color-red)]/10 hover:text-[var(--color-red)]"
								>
									Delete
								</button>
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/if}
{/snippet}

{#snippet emptyState(type: 'platform' | 'byoc')}
	<div class="flex flex-col items-center justify-center py-28 text-center">
		<!-- Floating icon with glow ring -->
		<div class="relative mb-7">
			<div class="absolute -inset-3 rounded-2xl bg-[var(--color-accent-glow)] blur-xl"></div>
			<div class="empty-icon-float relative flex h-18 w-18 items-center justify-center rounded-xl border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] shadow-card">
				<svg width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.25" stroke-linecap="round" stroke-linejoin="round" class="text-[var(--color-accent-mid)]"><rect x="2" y="2" width="20" height="8" rx="2"/><rect x="2" y="14" width="20" height="8" rx="2"/><line x1="6" y1="6" x2="6.01" y2="6"/><line x1="6" y1="18" x2="6.01" y2="18"/></svg>
			</div>
		</div>
		<p class="font-serif text-heading leading-snug text-[var(--color-text-secondary)]">
			{type === 'platform' ? 'No platform hosts yet' : 'No BYOC hosts across any team'}
		</p>
		<p class="mt-2 max-w-[320px] text-ui text-[var(--color-text-muted)]">
			{type === 'platform'
				? 'Add a host to start scheduling capsules onto your own compute.'
				: 'Teams that register their own compute will appear here.'}
		</p>
		{#if type === 'platform'}
			<button
				onclick={() => { showCreate = true; createError = null; createForm = { provider: '', availability_zone: '' }; }}
				class="mt-6 flex items-center gap-2 rounded-[var(--radius-button)] border border-[var(--color-accent)]/30 bg-[var(--color-accent)]/10 px-4 py-2 text-ui font-medium text-[var(--color-accent-bright)] transition-all duration-200 hover:bg-[var(--color-accent)]/20 hover:border-[var(--color-accent)]/50"
			>
				<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>
				Add your first host
			</button>
		{/if}
	</div>
{/snippet}

<!-- Add Platform Host Dialog -->
{#if showCreate}
	<div class="fixed inset-0 z-50 flex items-center justify-center">
		<div
			class="absolute inset-0 bg-black/60"
			role="button"
			tabindex="-1"
			onclick={() => { if (!creating) showCreate = false; }}
			onkeydown={(e) => { if (e.key === 'Escape' && !creating) showCreate = false; }}
		></div>
		<div
			class="relative w-full max-w-[420px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] shadow-dialog"
			style="animation: fadeUp 0.18s cubic-bezier(0.25,1,0.5,1) both"
		>
			<!-- Top accent edge -->
			<div class="h-[2px] rounded-t-[var(--radius-card)] bg-gradient-to-r from-transparent via-[var(--color-accent)] to-transparent"></div>

			<div class="p-6">
			<h2 class="font-serif text-heading leading-tight text-[var(--color-text-bright)]">
				Add Platform Host
			</h2>
			<p class="mt-1.5 text-ui text-[var(--color-text-tertiary)]">
				Register a new platform-managed host. You'll receive a one-time registration token.
			</p>

			{#if createError}
				<div class="mt-4 rounded-[var(--radius-input)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-3 py-2 text-meta text-[var(--color-red)]">
					{createError}
				</div>
			{/if}

			<div class="mt-5 space-y-4">
				<div>
					<label class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]" for="platform-provider">
						Provider <span class="normal-case font-normal text-[var(--color-text-muted)]">(optional)</span>
					</label>
					<input
						id="platform-provider"
						type="text"
						placeholder="e.g. aws, gcp, bare-metal"
						bind:value={createForm.provider}
						disabled={creating}
						class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-4)] px-3 py-2 text-ui text-[var(--color-text-bright)] outline-none placeholder:text-[var(--color-text-muted)] transition-colors duration-150 focus:border-[var(--color-accent)] disabled:opacity-60"
					/>
				</div>
				<div>
					<label class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]" for="platform-az">
						Availability Zone <span class="normal-case font-normal text-[var(--color-text-muted)]">(optional)</span>
					</label>
					<input
						id="platform-az"
						type="text"
						placeholder="e.g. us-east-1a"
						bind:value={createForm.availability_zone}
						disabled={creating}
						class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-4)] px-3 py-2 text-ui text-[var(--color-text-bright)] outline-none placeholder:text-[var(--color-text-muted)] transition-colors duration-150 focus:border-[var(--color-accent)] disabled:opacity-60"
					/>
				</div>
			</div>

			<div class="mt-6 flex justify-end gap-3">
				<button
					onclick={() => (showCreate = false)}
					disabled={creating}
					class="rounded-[var(--radius-button)] border border-[var(--color-border)] px-4 py-2 text-ui text-[var(--color-text-secondary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)] disabled:opacity-50"
				>
					Cancel
				</button>
				<button
					onclick={handleCreatePlatform}
					disabled={creating}
					class="group flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-accent)] px-5 py-2.5 text-ui font-semibold text-white transition-all duration-200 hover:shadow-[0_0_20px_var(--color-accent-glow-mid)] hover:brightness-115 hover:-translate-y-px active:translate-y-0 disabled:opacity-50 disabled:hover:translate-y-0 disabled:hover:shadow-none"
				>
					{#if creating}
						<svg class="animate-spin" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 12a9 9 0 1 1-6.219-8.56"/></svg>
						Adding…
					{:else}
						Add Host
					{/if}
				</button>
			</div>
			</div>
		</div>
	</div>
{/if}

<!-- Token reveal -->
{#if createdResult}
	<div class="fixed inset-0 z-50 flex items-center justify-center">
		<div class="absolute inset-0 bg-black/60"></div>
		<div
			class="relative w-full max-w-[500px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] shadow-dialog"
			style="animation: fadeUp 0.18s cubic-bezier(0.25,1,0.5,1) both"
		>
			<!-- Success accent edge -->
			<div class="h-[2px] rounded-t-[var(--radius-card)] bg-gradient-to-r from-transparent via-[var(--color-accent-bright)] to-transparent"></div>

			<div class="p-6">
			<!-- Animated checkmark -->
			<div class="mb-5 flex h-12 w-12 items-center justify-center rounded-full bg-[var(--color-accent-glow-mid)]">
				<svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="var(--color-accent-bright)" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
					<polyline
						points="20 6 9 17 4 12"
						class="checkmark-path"
						class:checkmark-drawn={checkmarkVisible}
					/>
				</svg>
			</div>

			<h2 class="font-serif text-heading leading-tight text-[var(--color-text-bright)]">
				Host registered
			</h2>
			<p class="mt-1.5 text-ui text-[var(--color-text-tertiary)]">
				Pass this token to the host agent to complete registration. It expires in
				<strong class="font-semibold text-[var(--color-amber)]">1 hour</strong> and is single-use.
			</p>

			<div class="mt-5 rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-0)] p-3">
				<div class="flex items-start gap-2">
					<code class="flex-1 break-all font-mono text-[0.8rem] leading-relaxed text-[var(--color-text-primary)]">
						{createdResult.registration_token}
					</code>
					<button
						onclick={() => copyToken(createdResult!.registration_token)}
						class="shrink-0 rounded-[var(--radius-button)] px-2.5 py-1.5 text-label font-semibold transition-all duration-200 {tokenCopied
							? 'bg-[var(--color-accent-glow)] text-[var(--color-accent-bright)] scale-95'
							: 'bg-[var(--color-bg-5)] text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]'}"
					>
						{tokenCopied ? '✓ Copied' : 'Copy'}
					</button>
				</div>
			</div>

			<div class="mt-3 flex items-start gap-2 rounded-[var(--radius-input)] border border-[var(--color-amber)]/30 bg-[var(--color-amber)]/6 px-3 py-2.5">
				<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="var(--color-amber)" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="mt-0.5 shrink-0"><path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3Z"/><line x1="12" y1="9" x2="12" y2="13"/><line x1="12" y1="17" x2="12.01" y2="17"/></svg>
				<p class="text-meta text-[var(--color-amber)]">
					This token will not be shown again. Store it safely before closing.
				</p>
			</div>

			<div class="mt-6">
				<button
					onclick={closeTokenReveal}
					class="w-full rounded-[var(--radius-button)] bg-[var(--color-bg-4)] px-4 py-2.5 text-ui font-medium text-[var(--color-text-primary)] transition-all duration-150 hover:bg-[var(--color-bg-5)]"
				>
					Done
				</button>
			</div>
			</div>
		</div>
	</div>
{/if}

<!-- Delete confirmation -->
{#if deleteTarget}
	<div class="fixed inset-0 z-50 flex items-center justify-center">
		<div
			class="absolute inset-0 bg-black/60"
			role="button"
			tabindex="-1"
			onclick={() => { if (!deleting) deleteTarget = null; }}
			onkeydown={(e) => { if (e.key === 'Escape' && !deleting) deleteTarget = null; }}
		></div>
		<div
			class="relative w-full max-w-[420px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] shadow-dialog"
			style="animation: fadeUp 0.18s cubic-bezier(0.25,1,0.5,1) both"
		>
			<!-- Danger accent edge -->
			<div class="h-[2px] rounded-t-[var(--radius-card)] bg-gradient-to-r from-transparent via-[var(--color-red)] to-transparent"></div>

			<div class="p-6">
			<h2 class="font-serif text-heading leading-tight text-[var(--color-text-bright)]">
				Delete Host
			</h2>
			<p class="mt-1.5 text-ui text-[var(--color-text-tertiary)]">
				Permanently remove <code class="rounded bg-[var(--color-bg-4)] px-1.5 py-0.5 font-mono text-[0.8rem] text-[var(--color-text-primary)]">{deleteTarget.id}</code>.
			</p>

			{#if deletePreviewLoading}
				<div class="mt-4 flex items-center gap-2 text-meta text-[var(--color-text-muted)]">
					<svg class="animate-spin" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 12a9 9 0 1 1-6.219-8.56"/></svg>
					Checking active capsules…
				</div>
			{:else if deletePreviewCapsules.length > 0}
				<div class="mt-4 rounded-[var(--radius-input)] border border-[var(--color-amber)]/30 bg-[var(--color-amber)]/6 px-3 py-2.5">
					<p class="text-meta font-semibold text-[var(--color-amber)]">
						{deletePreviewCapsules.length} active capsule{deletePreviewCapsules.length === 1 ? '' : 's'} will be destroyed.
					</p>
					<p class="mt-0.5 text-meta text-[var(--color-amber)]/70">
						All running workloads on this host will be terminated immediately.
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
					disabled={deleting || deletePreviewLoading}
					class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-red)] px-5 py-2.5 text-ui font-semibold text-white transition-all duration-200 hover:shadow-[0_0_16px_rgba(207,129,114,0.25)] hover:brightness-110 disabled:opacity-50"
				>
					{#if deleting}
						<svg class="animate-spin" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 12a9 9 0 1 1-6.219-8.56"/></svg>
						Deleting…
					{:else}
						{deletePreviewCapsules.length > 0 ? 'Force Delete' : 'Delete'}
					{/if}
				</button>
			</div>
			</div>
		</div>
	</div>
{/if}

<style>
	@keyframes fadeUp {
		from { opacity: 0; transform: translateY(10px); }
		to { opacity: 1; transform: translateY(0); }
	}

	@keyframes slideIn {
		from { opacity: 0; transform: translateX(-6px); }
		to { opacity: 1; transform: translateX(0); }
	}

	@keyframes shimmer {
		0% { background-position: -200% 0; }
		100% { background-position: 200% 0; }
	}

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

	.new-row {
		animation: slideIn 0.3s cubic-bezier(0.25, 1, 0.5, 1) both;
	}

	/* Checkmark draw animation */
	.checkmark-path {
		stroke-dasharray: 30;
		stroke-dashoffset: 30;
		transition: stroke-dashoffset 0.4s cubic-bezier(0.25, 1, 0.5, 1) 0.1s;
	}

	.checkmark-drawn {
		stroke-dashoffset: 0;
	}

	/* Stat pill — shared base */
	.stat-pill {
		display: flex;
		align-items: baseline;
		gap: 6px;
		border-radius: var(--radius-button);
		border-width: 1px;
		padding: 6px 12px;
		transition: transform 0.15s ease, box-shadow 0.15s ease;
	}
	.stat-pill:hover {
		transform: translateY(-1px);
		box-shadow: 0 2px 8px rgba(0, 0, 0, 0.25);
	}

	/* Table header */
	.table-header {
		padding: 10px 20px;
		text-align: left;
		font-size: var(--text-label);
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.06em;
		color: var(--color-text-tertiary);
	}

	/* Staggered row entrance */
	.table-row-animate {
		animation: fadeUp 0.25s ease both;
	}

	/* Tab button */
	.tab-button {
		position: relative;
		padding: 14px 20px 14px 0;
		font-size: var(--text-ui);
		transition: color 0.15s ease;
		cursor: pointer;
	}

	/* Online host row — subtle left accent */
	.host-row-online {
		box-shadow: inset 3px 0 0 var(--color-accent);
	}

	/* Empty state icon float */
	.empty-icon-float {
		animation: iconFloat 3s ease-in-out infinite;
	}
</style>
