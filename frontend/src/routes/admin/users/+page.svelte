<script lang="ts">
	import { onMount } from 'svelte';
	import { toast } from '$lib/toast.svelte';
	import { formatDate } from '$lib/utils/format';
	import {
		listAdminUsers,
		setUserActive,
		type AdminUser,
	} from '$lib/api/admin-users';

	// Data state
	let users = $state<AdminUser[]>([]);
	let loading = $state(true);
	let error = $state<string | null>(null);
	let currentPage = $state(1);
	let totalPages = $state(1);
	let totalUsers = $state(0);

	// Animation
	let initialAnimationDone = $state(false);

	// Toggle state
	let togglingId = $state<string | null>(null);

	async function fetchUsers(page: number = 1) {
		const wasEmpty = users.length === 0;
		if (wasEmpty) loading = true;
		error = null;

		const result = await listAdminUsers(page);
		if (result.ok) {
			users = result.data.users;
			currentPage = result.data.page;
			totalPages = result.data.total_pages;
			totalUsers = result.data.total;
		} else {
			error = result.error;
		}
		loading = false;

		if (!initialAnimationDone) {
			setTimeout(() => { initialAnimationDone = true; }, 400 + (users.length * 30));
		}
	}

	async function handleToggleActive(user: AdminUser) {
		togglingId = user.id;
		const newActive = user.status !== 'active';
		const result = await setUserActive(user.id, newActive);
		if (result.ok) {
			user.status = newActive ? 'active' : 'disabled';
			toast.success(`${user.email} ${newActive ? 'activated' : 'deactivated'}`);
		} else {
			toast.error(result.error);
		}
		togglingId = null;
	}

	function goToPage(page: number) {
		if (page < 1 || page > totalPages) return;
		fetchUsers(page);
	}

	onMount(() => {
		fetchUsers();
	});
</script>

<svelte:head>
	<title>Wrenn Admin — Users</title>
</svelte:head>

<style>
	.user-grid {
		display: grid;
		grid-template-columns: 1.6fr 1.4fr 0.7fr 0.7fr 0.5fr 1fr 0.6fr;
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
	.user-row:hover .row-stripe {
		transform: scaleY(1);
	}

	@keyframes fadeUp {
		from { opacity: 0; transform: translateY(10px); }
		to { opacity: 1; transform: translateY(0); }
	}
</style>

<main class="flex min-w-0 flex-1 flex-col overflow-hidden">
	<!-- Header -->
	<header class="relative shrink-0 border-b border-[var(--color-border)] bg-[var(--color-bg-1)]">
			<div class="absolute inset-0 bg-gradient-to-b from-[var(--color-accent)]/[0.02] to-transparent pointer-events-none"></div>

			<div class="relative flex items-start justify-between px-8 pt-7 pb-5">
				<div>
					<h1 class="font-serif text-page leading-none text-[var(--color-text-bright)]">
						Users
					</h1>
					<p class="mt-2 text-ui text-[var(--color-text-tertiary)]">
						All registered users, team memberships, and account status.
					</p>
				</div>
			</div>

			<!-- Stat strip -->
			{#if !loading && !error}
				<div class="relative flex items-center gap-3 px-8 pb-5">
					<div class="stat-pill border-[var(--color-border)] bg-[var(--color-bg-2)]">
						<span class="font-mono text-body font-bold tabular-nums text-[var(--color-text-bright)]">{totalUsers}</span>
						<span class="text-label text-[var(--color-text-muted)]">user{totalUsers !== 1 ? 's' : ''}</span>
					</div>
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
				<div class="user-grid rounded-t-[var(--radius-card)] border-b border-[var(--color-border)] bg-[var(--color-bg-3)]">
					<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Name</div>
					<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Email</div>
					<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Teams</div>
					<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Owned</div>
					<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Role</div>
					<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Joined</div>
					<div class="px-5 py-3 text-right text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Status</div>
				</div>

				{#if loading && users.length === 0}
					<div class="flex items-center justify-center py-16">
						<div class="flex items-center gap-3 text-ui text-[var(--color-text-secondary)]">
							<svg class="animate-spin" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
								<path d="M21 12a9 9 0 1 1-6.219-8.56" />
							</svg>
							Loading users...
						</div>
					</div>
				{:else if users.length === 0}
					<div class="flex flex-col items-center justify-center py-[72px]">
						<div class="relative mb-5">
							<div class="absolute inset-0 -m-4 rounded-full" style="background: radial-gradient(circle, rgba(94,140,88,0.08) 0%, transparent 70%)"></div>
							<div class="relative flex h-14 w-14 items-center justify-center rounded-[var(--radius-card)] border border-[var(--color-accent)]/20 bg-[var(--color-bg-3)]">
								<svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="var(--color-accent-mid)" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
									<path d="M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2" /><circle cx="12" cy="7" r="4" />
								</svg>
							</div>
						</div>
						<p class="font-serif text-heading text-[var(--color-text-bright)]">
							No users yet
						</p>
						<p class="mt-1.5 text-ui text-[var(--color-text-tertiary)]">
							Users appear here when they sign up.
						</p>
					</div>
				{:else}
					{#each users as user, i (user.id)}
						<div
							class="user-row user-grid relative items-center overflow-hidden border-b border-[var(--color-border)] transition-colors duration-150 last:border-b-0 {user.status !== 'active' ? 'opacity-50' : 'hover:bg-[var(--color-bg-3)]'}"
							style={initialAnimationDone ? '' : `animation: fadeUp 0.35s ease both; animation-delay: ${i * 30}ms`}
						>
							<!-- Left accent stripe -->
							{#if user.status === 'active'}
								<div class="row-stripe pointer-events-none absolute left-0 top-0 h-full w-0.5 bg-[var(--color-accent)]"></div>
							{/if}

							<!-- Name -->
							<div class="min-w-0 px-5 py-4">
								<div class="flex items-center gap-2">
									<span class="block truncate text-ui font-medium text-[var(--color-text-bright)]">{user.name || '\u2014'}</span>
									{#if user.is_admin}
										<span class="inline-flex shrink-0 items-center rounded-full border border-[var(--color-amber)]/30 bg-[var(--color-amber)]/10 px-2 py-0.5 text-[10px] font-semibold uppercase tracking-[0.05em] text-[var(--color-amber)]">
											Admin
										</span>
									{/if}
								</div>
								<span class="block truncate font-mono text-label text-[var(--color-text-muted)]">{user.id}</span>
							</div>

							<!-- Email -->
							<div class="min-w-0 px-5 py-4">
								<span class="block truncate font-mono text-ui text-[var(--color-text-secondary)]">{user.email}</span>
							</div>

							<!-- Teams Joined -->
							<div class="px-5 py-4">
								<span class="font-mono text-ui text-[var(--color-text-secondary)]">{user.teams_joined}</span>
							</div>

							<!-- Teams Owned -->
							<div class="px-5 py-4">
								<span class="font-mono text-ui text-[var(--color-text-secondary)]">{user.teams_owned}</span>
							</div>

							<!-- Role -->
							<div class="px-5 py-4">
								<span class="text-ui text-[var(--color-text-secondary)]">{user.is_admin ? 'Admin' : 'User'}</span>
							</div>

							<!-- Joined -->
							<div class="px-5 py-4">
								<span class="text-ui text-[var(--color-text-secondary)]">{formatDate(user.created_at)}</span>
							</div>

							<!-- Status / Toggle -->
							<div class="flex items-center justify-end px-5 py-4">
								<button
									onclick={() => handleToggleActive(user)}
									disabled={togglingId === user.id}
									class="rounded-[var(--radius-button)] border px-3 py-1.5 text-meta font-medium transition-all duration-150 disabled:opacity-50
										{user.status === 'active'
											? 'border-[var(--color-accent)]/30 bg-[var(--color-accent)]/8 text-[var(--color-accent-bright)] hover:bg-[var(--color-accent)]/15 hover:border-[var(--color-accent)]/50'
											: 'border-[var(--color-red)]/30 bg-[var(--color-red)]/8 text-[var(--color-red)] hover:bg-[var(--color-red)]/15 hover:border-[var(--color-red)]/50'}"
								>
									{#if togglingId === user.id}
										<svg class="inline animate-spin" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 12a9 9 0 1 1-6.219-8.56"/></svg>
									{:else}
										{user.status === 'active' ? 'Active' : user.status.charAt(0).toUpperCase() + user.status.slice(1)}
									{/if}
								</button>
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
