<script lang="ts">
	import AdminSidebar from '$lib/components/AdminSidebar.svelte';
	import { onMount } from 'svelte';
	import { toast } from '$lib/toast.svelte';
	import { formatDate } from '$lib/utils/format';
	import {
		listAdminTeams,
		adminSetBYOC,
		adminDeleteTeam,
		type AdminTeam,
		type AdminTeamsResponse
	} from '$lib/api/team';

	let collapsed = $state(
		typeof window !== 'undefined'
			? localStorage.getItem('wrenn_sidebar_collapsed') === 'true'
			: false
	);

	// Data state
	let teams = $state<AdminTeam[]>([]);
	let loading = $state(true);
	let error = $state<string | null>(null);
	let currentPage = $state(1);
	let totalPages = $state(1);
	let totalTeams = $state(0);

	// Delete state
	let deleteTarget = $state<AdminTeam | null>(null);
	let deleting = $state(false);
	let deleteError = $state<string | null>(null);

	// BYOC toggle state
	let byocTarget = $state<AdminTeam | null>(null);
	let enablingByoc = $state(false);
	let byocError = $state<string | null>(null);

	// Animation
	let initialAnimationDone = $state(false);

	// Stats
	let byocCount = $derived(teams.filter((t) => t.is_byoc).length);
	let totalActiveSandboxes = $derived(teams.reduce((sum, t) => sum + t.active_sandbox_count, 0));

	async function fetchTeams(page: number = 1) {
		const wasEmpty = teams.length === 0;
		if (wasEmpty) loading = true;
		error = null;

		const result = await listAdminTeams(page);
		if (result.ok) {
			teams = result.data.teams;
			currentPage = result.data.page;
			totalPages = result.data.total_pages;
			totalTeams = result.data.total;
		} else {
			error = result.error;
		}
		loading = false;

		if (!initialAnimationDone) {
			setTimeout(() => { initialAnimationDone = true; }, 400 + (teams.length * 30));
		}
	}

	async function handleEnableBYOC() {
		if (!byocTarget) return;
		enablingByoc = true;
		byocError = null;

		const result = await adminSetBYOC(byocTarget.id, true);
		if (result.ok) {
			byocTarget.is_byoc = true;
			toast.success(`BYOC enabled for ${byocTarget.name}`);
			byocTarget = null;
		} else {
			byocError = result.error;
		}
		enablingByoc = false;
	}

	async function handleDelete() {
		if (!deleteTarget) return;
		deleting = true;
		deleteError = null;
		const name = deleteTarget.name;
		const result = await adminDeleteTeam(deleteTarget.id);
		if (result.ok) {
			teams = teams.filter((t) => t.id !== deleteTarget!.id);
			totalTeams--;
			deleteTarget = null;
			toast.success(`Team "${name}" deleted`);
		} else {
			deleteError = result.error;
		}
		deleting = false;
	}

	function goToPage(page: number) {
		if (page < 1 || page > totalPages) return;
		fetchTeams(page);
	}

	onMount(() => {
		fetchTeams();
	});
</script>

<svelte:head>
	<title>Wrenn Admin — Teams</title>
</svelte:head>

<style>
	.team-grid {
		display: grid;
		grid-template-columns: 1.4fr 0.5fr 1.4fr 0.6fr 0.6fr 0.5fr 1fr 0.5fr;
	}

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

	.row-stripe {
		transform: scaleY(0);
		transform-origin: center;
		transition: transform 0.18s cubic-bezier(0.25, 1, 0.5, 1);
	}
	.team-row:hover .row-stripe {
		transform: scaleY(1);
	}

	@keyframes fadeUp {
		from { opacity: 0; transform: translateY(10px); }
		to { opacity: 1; transform: translateY(0); }
	}
</style>

<div class="flex h-screen overflow-hidden bg-[var(--color-bg-0)]">
	<AdminSidebar bind:collapsed />

	<main class="flex min-w-0 flex-1 flex-col overflow-hidden">
		<!-- Header -->
		<header class="relative shrink-0 border-b border-[var(--color-border)] bg-[var(--color-bg-1)]">
			<div class="absolute inset-0 bg-gradient-to-b from-[var(--color-accent)]/[0.02] to-transparent pointer-events-none"></div>

			<div class="relative flex items-start justify-between px-8 pt-7 pb-5">
				<div>
					<h1 class="font-serif text-page leading-none text-[var(--color-text-bright)]">
						Teams
					</h1>
					<p class="mt-2 text-ui text-[var(--color-text-tertiary)]">
						All registered teams, BYOC status, and active capsules.
					</p>
				</div>
			</div>

			<!-- Stat strip -->
			{#if !loading && !error}
				<div class="relative flex items-center gap-3 px-8 pb-5">
					<div class="stat-pill border-[var(--color-border)] bg-[var(--color-bg-2)]">
						<span class="font-mono text-body font-bold tabular-nums text-[var(--color-text-bright)]">{totalTeams}</span>
						<span class="text-label text-[var(--color-text-muted)]">team{totalTeams !== 1 ? 's' : ''}</span>
					</div>
					{#if byocCount > 0}
						<div class="stat-pill border-[var(--color-accent)]/25 bg-[var(--color-accent)]/8">
							<span class="font-mono text-body font-bold tabular-nums text-[var(--color-accent-bright)]">{byocCount}</span>
							<span class="text-label text-[var(--color-accent-bright)]/70">BYOC</span>
						</div>
					{/if}
					{#if totalActiveSandboxes > 0}
						<div class="stat-pill border-[var(--color-accent)]/25 bg-[var(--color-accent)]/8 gap-2">
							<span class="relative flex h-2 w-2 shrink-0">
								<span class="animate-status-ping absolute inline-flex h-full w-full rounded-full bg-[var(--color-accent)] opacity-60"></span>
								<span class="relative inline-flex h-2 w-2 rounded-full bg-[var(--color-accent)]"></span>
							</span>
							<span class="font-mono text-body font-bold tabular-nums text-[var(--color-accent-bright)]">{totalActiveSandboxes}</span>
							<span class="text-label text-[var(--color-accent-bright)]/70">active</span>
						</div>
					{/if}
				</div>
			{/if}
		</header>

		<!-- Content -->
		<div class="flex-1 overflow-y-auto px-8 py-6" style="animation: fadeUp 0.35s ease both">
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
				<div class="team-grid rounded-t-[var(--radius-card)] border-b border-[var(--color-border)] bg-[var(--color-bg-3)]">
					<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Name</div>
					<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Members</div>
					<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Owner</div>
					<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">BYOC</div>
					<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Capsules</div>
					<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Channels</div>
					<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Created</div>
					<div class="px-5 py-3 text-right text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Actions</div>
				</div>

				{#if loading && teams.length === 0}
					<div class="flex items-center justify-center py-16">
						<div class="flex items-center gap-3 text-ui text-[var(--color-text-secondary)]">
							<svg class="animate-spin" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
								<path d="M21 12a9 9 0 1 1-6.219-8.56" />
							</svg>
							Loading teams...
						</div>
					</div>
				{:else if teams.length === 0}
					<div class="flex flex-col items-center justify-center py-[72px]">
						<div class="relative mb-5">
							<div class="absolute inset-0 -m-4 rounded-full" style="background: radial-gradient(circle, rgba(94,140,88,0.08) 0%, transparent 70%)"></div>
							<div class="relative flex h-14 w-14 items-center justify-center rounded-[var(--radius-card)] border border-[var(--color-accent)]/20 bg-[var(--color-bg-3)]">
								<svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="var(--color-accent-mid)" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
									<path d="M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2" /><circle cx="9" cy="7" r="4" /><path d="M23 21v-2a4 4 0 0 0-3-3.87" /><path d="M16 3.13a4 4 0 0 1 0 7.75" />
								</svg>
							</div>
						</div>
						<p class="font-serif text-heading text-[var(--color-text-bright)]">
							No teams yet
						</p>
						<p class="mt-1.5 text-ui text-[var(--color-text-tertiary)]">
							Teams are created when users sign up.
						</p>
					</div>
				{:else}
					{#each teams as team, i (team.id)}
						{@const isDeleted = !!team.deleted_at}
						<div
							class="team-row team-grid relative items-center overflow-hidden border-b border-[var(--color-border)] transition-colors duration-150 last:border-b-0 {isDeleted ? 'opacity-50' : 'hover:bg-[var(--color-bg-3)]'}"
							style={initialAnimationDone ? '' : `animation: fadeUp 0.35s ease both; animation-delay: ${i * 30}ms`}
						>
							<!-- Left accent stripe -->
							{#if !isDeleted}
								<div class="row-stripe pointer-events-none absolute left-0 top-0 h-full w-0.5 bg-[var(--color-accent)]"></div>
							{/if}

							<!-- Name -->
							<div class="min-w-0 px-5 py-4">
								<div class="flex items-center gap-2">
									<span class="block truncate text-ui font-medium text-[var(--color-text-bright)]">{team.name}</span>
									{#if isDeleted}
										<span class="inline-flex shrink-0 items-center rounded-full border border-[var(--color-red)]/30 bg-[var(--color-red)]/10 px-2 py-0.5 text-[10px] font-semibold uppercase tracking-[0.05em] text-[var(--color-red)]">
											Deleted
										</span>
									{/if}
								</div>
								<span class="block truncate font-mono text-label text-[var(--color-text-muted)]">{team.slug}</span>
							</div>

							<!-- Members -->
							<div class="px-5 py-4">
								<span class="font-mono text-ui text-[var(--color-text-secondary)]">{team.member_count}</span>
							</div>

							<!-- Owner -->
							<div class="min-w-0 px-5 py-4">
								{#if team.owner_name || team.owner_email}
									<span class="block truncate text-ui text-[var(--color-text-secondary)]">{team.owner_name || '\u2014'}</span>
									<span class="block truncate font-mono text-label text-[var(--color-text-muted)]">{team.owner_email}</span>
								{:else}
									<span class="text-ui text-[var(--color-text-muted)]">&mdash;</span>
								{/if}
							</div>

							<!-- BYOC -->
							<div class="px-5 py-4">
								{#if team.is_byoc}
									<span class="inline-flex items-center gap-1.5 rounded-full border border-[var(--color-accent)]/30 bg-[var(--color-accent)]/10 px-2.5 py-1 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-accent-bright)]">
										Enabled
									</span>
								{:else if !isDeleted}
									<button
										onclick={() => { byocTarget = team; byocError = null; }}
										class="inline-flex items-center gap-1.5 rounded-full border border-[var(--color-border)] bg-transparent px-2.5 py-1 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)] transition-all duration-150 hover:border-[var(--color-accent)]/40 hover:text-[var(--color-accent-mid)]"
									>
										Enable
									</button>
								{:else}
									<span class="text-ui text-[var(--color-text-muted)]">&mdash;</span>
								{/if}
							</div>

							<!-- Capsules -->
							<div class="px-5 py-4">
								{#if team.active_sandbox_count > 0}
									<span class="flex items-center gap-1.5">
										<span class="relative flex h-[6px] w-[6px] shrink-0">
											<span class="animate-status-ping absolute inline-flex h-full w-full rounded-full bg-[var(--color-accent)]"></span>
											<span class="relative inline-flex h-[6px] w-[6px] rounded-full bg-[var(--color-accent)]"></span>
										</span>
										<span class="font-mono text-ui text-[var(--color-accent-bright)]">{team.active_sandbox_count}</span>
									</span>
								{:else}
									<span class="font-mono text-ui text-[var(--color-text-muted)]">0</span>
								{/if}
							</div>

							<!-- Channels -->
							<div class="px-5 py-4">
								<span class="font-mono text-ui text-[var(--color-text-secondary)]">{team.channel_count}</span>
							</div>

							<!-- Created -->
							<div class="px-5 py-4">
								<span class="text-ui text-[var(--color-text-secondary)]">{formatDate(team.created_at)}</span>
							</div>

							<!-- Actions -->
							<div class="flex items-center justify-end px-5 py-4">
								{#if !isDeleted}
									<button
										onclick={() => { deleteTarget = team; deleteError = null; }}
										class="rounded-[var(--radius-button)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/8 px-3 py-1.5 text-meta font-medium text-[var(--color-red)] transition-all duration-150 hover:bg-[var(--color-red)]/15 hover:border-[var(--color-red)]/50"
									>
										Delete
									</button>
								{/if}
							</div>
						</div>
					{/each}
				{/if}
			</div>

			<!-- Pagination -->
			{#if totalPages > 1}
				<div class="mt-4 flex items-center justify-between">
					<span class="text-ui text-[var(--color-text-tertiary)]">
						Page <span class="font-mono text-[var(--color-text-secondary)]">{currentPage}</span> of <span class="font-mono text-[var(--color-text-secondary)]">{totalPages}</span>
					</span>
					<div class="flex items-center gap-2">
						<button
							onclick={() => goToPage(currentPage - 1)}
							disabled={currentPage <= 1}
							class="flex items-center gap-1.5 rounded-[var(--radius-button)] border border-[var(--color-border)] px-3 py-1.5 text-ui text-[var(--color-text-secondary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)] disabled:opacity-40 disabled:hover:border-[var(--color-border)] disabled:hover:text-[var(--color-text-secondary)]"
						>
							<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="15 18 9 12 15 6"/></svg>
							Previous
						</button>
						<button
							onclick={() => goToPage(currentPage + 1)}
							disabled={currentPage >= totalPages}
							class="flex items-center gap-1.5 rounded-[var(--radius-button)] border border-[var(--color-border)] px-3 py-1.5 text-ui text-[var(--color-text-secondary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)] disabled:opacity-40 disabled:hover:border-[var(--color-border)] disabled:hover:text-[var(--color-text-secondary)]"
						>
							Next
							<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="9 18 15 12 9 6"/></svg>
						</button>
					</div>
				</div>
			{/if}
		</div>

		<!-- Status bar -->
		<footer class="flex h-7 shrink-0 items-center justify-end border-t border-[var(--color-border)] bg-[var(--color-bg-1)] px-8">
			<div class="flex items-center gap-1.5">
				<span class="relative flex h-[5px] w-[5px]">
					<span class="animate-status-ping absolute inline-flex h-full w-full rounded-full bg-[var(--color-accent)]"></span>
					<span class="relative inline-flex h-[5px] w-[5px] rounded-full bg-[var(--color-accent)]"></span>
				</span>
				<span class="font-mono text-label uppercase tracking-[0.04em] text-[var(--color-text-secondary)]">All systems operational</span>
			</div>
		</footer>
	</main>
</div>

<!-- BYOC confirmation dialog -->
{#if byocTarget}
	<div class="fixed inset-0 z-50 flex items-center justify-center">
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="absolute inset-0 bg-black/60"
			onclick={() => { if (!enablingByoc) byocTarget = null; }}
			onkeydown={(e) => { if (e.key === 'Escape' && !enablingByoc) byocTarget = null; }}
		></div>
		<div
			class="relative w-full max-w-[420px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)]"
			style="animation: fadeUp 0.2s ease both; box-shadow: var(--shadow-dialog)"
		>
			<div class="p-6">
				<h2 class="font-serif text-heading leading-tight text-[var(--color-text-bright)]">
					Enable BYOC
				</h2>
				<p class="mt-1.5 text-ui text-[var(--color-text-tertiary)]">
					Allow <code class="rounded bg-[var(--color-bg-4)] px-1.5 py-0.5 font-mono text-[0.8rem] text-[var(--color-text-primary)]">{byocTarget.name}</code> to register and run capsules on their own hosts.
				</p>

				<div class="mt-3 flex items-start gap-2.5 rounded-[var(--radius-input)] border border-[var(--color-amber)]/30 bg-[var(--color-amber)]/5 px-3 py-2.5">
					<svg class="mt-0.5 shrink-0 text-[var(--color-amber)]" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
						<path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z" /><line x1="12" y1="9" x2="12" y2="13" /><line x1="12" y1="17" x2="12.01" y2="17" />
					</svg>
					<span class="text-meta text-[var(--color-amber)]">
						BYOC cannot be disabled once enabled.
					</span>
				</div>

				{#if byocError}
					<div class="mt-3 rounded-[var(--radius-input)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-3 py-2 text-meta text-[var(--color-red)]">
						{byocError}
					</div>
				{/if}

				<div class="mt-6 flex justify-end gap-3">
					<button
						onclick={() => (byocTarget = null)}
						disabled={enablingByoc}
						class="rounded-[var(--radius-button)] border border-[var(--color-border)] px-4 py-2 text-ui text-[var(--color-text-secondary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)] disabled:opacity-50"
					>
						Cancel
					</button>
					<button
						onclick={handleEnableBYOC}
						disabled={enablingByoc}
						class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-accent)] px-5 py-2 text-ui font-semibold text-white transition-all duration-150 hover:brightness-110 hover:-translate-y-px active:translate-y-0 disabled:opacity-50 disabled:hover:translate-y-0"
					>
						{#if enablingByoc}
							<svg class="animate-spin" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 12a9 9 0 1 1-6.219-8.56"/></svg>
							Enabling...
						{:else}
							Enable BYOC
						{/if}
					</button>
				</div>
			</div>
		</div>
	</div>
{/if}

<!-- Delete confirmation dialog -->
{#if deleteTarget}
	<div class="fixed inset-0 z-50 flex items-center justify-center">
		<!-- svelte-ignore a11y_no_static_element_interactions -->
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
				<h2 class="font-serif text-heading leading-tight text-[var(--color-text-bright)]">
					Delete Team
				</h2>
				<p class="mt-1.5 text-ui text-[var(--color-text-tertiary)]">
					Remove <code class="rounded bg-[var(--color-bg-4)] px-1.5 py-0.5 font-mono text-[0.8rem] text-[var(--color-text-primary)]">{deleteTarget.name}</code> and stop all its running capsules. Members will lose access immediately.
				</p>

				{#if deleteTarget.active_sandbox_count > 0}
					<div class="mt-3 flex items-start gap-2.5 rounded-[var(--radius-input)] border border-[var(--color-amber)]/30 bg-[var(--color-amber)]/5 px-3 py-2.5">
						<svg class="mt-0.5 shrink-0 text-[var(--color-amber)]" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
							<path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z" /><line x1="12" y1="9" x2="12" y2="13" /><line x1="12" y1="17" x2="12.01" y2="17" />
						</svg>
						<span class="text-meta text-[var(--color-amber)]">
							<strong class="font-semibold">{deleteTarget.active_sandbox_count}</strong> active capsule{deleteTarget.active_sandbox_count !== 1 ? 's' : ''} will be destroyed immediately.
						</span>
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
							<svg class="animate-spin" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 12a9 9 0 1 1-6.219-8.56"/></svg>
							Deleting...
						{:else}
							Delete team
						{/if}
					</button>
				</div>
			</div>
		</div>
	</div>
{/if}
