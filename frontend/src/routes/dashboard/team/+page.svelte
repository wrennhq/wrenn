<script lang="ts">
	import Sidebar from '$lib/components/Sidebar.svelte';
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { fly } from 'svelte/transition';
	import { cubicOut } from 'svelte/easing';
	import { auth } from '$lib/auth.svelte';
	import { toast } from '$lib/toast.svelte';
	import {
		getTeam,
		listTeams,
		updateTeam,
		addMember,
		removeMember,
		updateMemberRole,
		deleteTeam,
		leaveTeam,
		switchTeam,
		searchUsers,
		type TeamInfo,
		type TeamMember,
		type UserSearchResult
	} from '$lib/api/team';
	import { teams as teamsStore } from '$lib/teams.svelte';

	let collapsed = $state(
		typeof window !== 'undefined'
			? localStorage.getItem('wrenn_sidebar_collapsed') === 'true'
			: false
	);

	// Page data
	let team = $state<TeamInfo | null>(null);
	let members = $state<TeamMember[]>([]);
	let loading = $state(true);
	let error = $state<string | null>(null);

	// True when this is the user's only team — deleting/leaving would leave them teamless
	let isLastTeam = $derived(teamsStore.list.length <= 1);

	// Current user's role — derived from members list
	let myRole = $derived(members.find((m) => m.user_id === auth.userId)?.role ?? 'member');
	let canManage = $derived(myRole === 'owner' || myRole === 'admin');

	// Inline name edit
	let editingName = $state(false);
	let editName = $state('');
	let savingName = $state(false);
	let nameError = $state<string | null>(null);
	let nameInputEl = $state<HTMLInputElement | null>(null);

	// Copy state
	let copiedId = $state(false);

	// Add member dialog
	let showAddMember = $state(false);
	let addEmail = $state('');
	let searchResults = $state<UserSearchResult[]>([]);
	let searchLoading = $state(false);
	let showResults = $state(false);
	let adding = $state(false);
	let addError = $state<string | null>(null);
	let searchTimeout: ReturnType<typeof setTimeout> | null = null;

	// Split button dropdown (members table)
	let openDropdownId = $state<string | null>(null);
	let dropdownPos = $state<{ top: number; left: number }>({ top: 0, left: 0 });

	// Role update (inline, no confirmation needed — reversible)
	let updatingRoleId = $state<string | null>(null);

	// Remove member confirmation
	let removeTarget = $state<TeamMember | null>(null);
	let removing = $state(false);
	let removeError = $state<string | null>(null);

	// Delight: new member row flash, name saved flash
	let recentlyAddedId = $state<string | null>(null);
	let nameSavedFlash = $state(false);

	// Danger zone
	let showDangerConfirm = $state(false);
	let dangerLoading = $state(false);
	let dangerError = $state<string | null>(null);

	async function fetchTeam() {
		loading = true;
		error = null;
		if (!auth.teamId) {
			loading = false;
			return;
		}
		const [teamResult] = await Promise.all([
			getTeam(auth.teamId),
			teamsStore.fetch()
		]);
		if (teamResult.ok) {
			team = teamResult.data.team;
			members = teamResult.data.members;
		} else {
			error = teamResult.error;
		}
		loading = false;
	}

	function startEditName() {
		if (!team || !canManage) return;
		editName = team.name;
		editingName = true;
		nameError = null;
		setTimeout(() => nameInputEl?.focus(), 0);
	}

	function cancelEditName() {
		editingName = false;
		nameError = null;
	}

	async function saveEditName() {
		if (!team) return;
		const trimmed = editName.trim();
		if (!trimmed || trimmed === team.name) {
			cancelEditName();
			return;
		}
		savingName = true;
		nameError = null;
		const result = await updateTeam(team.id, trimmed);
		if (result.ok) {
			team = { ...team, name: trimmed };
			editingName = false;
			nameSavedFlash = true;
			setTimeout(() => (nameSavedFlash = false), 900);
			toast.success('Team name updated');
		} else {
			nameError = result.error;
		}
		savingName = false;
	}

	async function copyToClipboard(text: string) {
		try {
			await navigator.clipboard.writeText(text);
			copiedId = true;
			setTimeout(() => (copiedId = false), 2000);
		} catch {
			toast.error('Copy failed — select the text and copy manually.');
		}
	}

	function handleSearchInput() {
		const val = addEmail.trim();
		showResults = false;
		if (searchTimeout) clearTimeout(searchTimeout);
		if (val.length < 3) {
			searchResults = [];
			return;
		}
		searchTimeout = setTimeout(async () => {
			searchLoading = true;
			const result = await searchUsers(val);
			if (result.ok) {
				searchResults = result.data;
				showResults = result.data.length > 0;
			}
			searchLoading = false;
		}, 300);
	}

	function selectSearchResult(user: UserSearchResult) {
		addEmail = user.email;
		showResults = false;
	}

	async function handleAddMember() {
		if (!team || !addEmail.trim()) return;
		adding = true;
		addError = null;
		const result = await addMember(team.id, addEmail.trim().toLowerCase());
		if (result.ok) {
			members = [...members, result.data];
			recentlyAddedId = result.data.user_id;
			setTimeout(() => (recentlyAddedId = null), 1200);
			showAddMember = false;
			addEmail = '';
			searchResults = [];
			showResults = false;
			toast.success('Member added');
		} else {
			addError = result.error;
		}
		adding = false;
	}

	async function handleUpdateRole(member: TeamMember, newRole: 'admin' | 'member') {
		if (!team) return;
		updatingRoleId = member.user_id;
		openDropdownId = null;
		const result = await updateMemberRole(team.id, member.user_id, newRole);
		if (result.ok) {
			members = members.map((m) =>
				m.user_id === member.user_id ? { ...m, role: newRole } : m
			);
			toast.success(
				newRole === 'admin'
					? `${member.name || member.email} is now an admin`
					: `${member.name || member.email} is now a member`
			);
		} else {
			toast.error(result.error);
		}
		updatingRoleId = null;
	}

	async function handleRemoveMember() {
		if (!team || !removeTarget) return;
		removing = true;
		removeError = null;
		const uid = removeTarget.user_id;
		const result = await removeMember(team.id, uid);
		if (result.ok) {
			members = members.filter((m) => m.user_id !== uid);
			removeTarget = null;
			toast.success('Member removed');
		} else {
			removeError = result.error;
		}
		removing = false;
	}

	async function handleDangerAction() {
		if (!team) return;
		dangerLoading = true;
		dangerError = null;
		const result = myRole === 'owner' ? await deleteTeam(team.id) : await leaveTeam(team.id);
		if (result.ok) {
			// Fetch remaining teams and switch to the first available one
			const teamsResult = await listTeams();
			const remaining = teamsResult.ok ? teamsResult.data : [];
			if (remaining.length > 0) {
				const switchResult = await switchTeam(remaining[0].id);
				if (switchResult.ok) {
					auth.login(switchResult.data);
					window.location.reload();
					return;
				}
			}
			// No teams left — prompt user to create one
			dangerLoading = false;
			showDangerConfirm = false;
			toast.error('No teams remaining. Use the sidebar to create a new team.');
		} else {
			dangerError = result.error;
			dangerLoading = false;
		}
	}

	function avatarColor(email: string): string {
		const palette = ['#5e8c58', '#5a9fd4', '#d4a73c', '#a07ab0', '#cf8172'];
		return palette[email.charCodeAt(0) % palette.length];
	}

	function formatDate(iso: string | undefined): string {
		if (!iso) return '—';
		return new Date(iso).toLocaleDateString('en-US', {
			month: 'short',
			day: 'numeric',
			year: 'numeric'
		});
	}

	function roleLabel(role: string): string {
		if (role === 'owner') return 'Owner';
		if (role === 'admin') return 'Admin';
		return 'Member';
	}

	// Whether to show actions for a given member row
	function canActOn(member: TeamMember): boolean {
		if (!canManage) return false;
		if (member.user_id === auth.userId) return false; // can't act on self
		if (member.role === 'owner') return false; // can't act on owner
		return true;
	}

	onMount(fetchTeam);
</script>

<svelte:head>
	<title>Wrenn — Team</title>
</svelte:head>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<svelte:window
	onkeydown={(e) => {
		if (e.key === 'Escape') {
			if (openDropdownId) {
				openDropdownId = null;
				return;
			}
			if (editingName && !savingName) {
				cancelEditName();
				return;
			}
			if (removing || adding || dangerLoading) return;
			removeTarget = null;
			if (!adding) showAddMember = false;
			showDangerConfirm = false;
		}
	}}
	onclick={(e) => {
		if (openDropdownId && !(e.target as Element)?.closest('.split-btn-container')) {
			openDropdownId = null;
		}
		if (showResults && !(e.target as Element)?.closest('.search-container')) {
			showResults = false;
		}
	}}
/>

<div class="flex h-screen overflow-hidden">
	<Sidebar bind:collapsed />

	<div class="flex flex-1 flex-col overflow-hidden">
		<main class="flex-1 overflow-y-auto bg-[var(--color-bg-0)]">
			<!-- Header -->
			<div class="px-7 pt-8">
				<h1 class="font-serif text-page tracking-[-0.02em] text-[var(--color-text-bright)]">
					Team
				</h1>
				<p class="mt-2 text-ui text-[var(--color-text-secondary)]">
					Members, roles, and workspace configuration for your team.
				</p>
				<div class="mt-6 border-b border-[var(--color-border)]"></div>
			</div>

			<!-- Content -->
			<div class="p-8" style="animation: fadeUp 0.35s ease both">
				{#if error}
					<div
						class="mb-6 flex items-center justify-between gap-4 rounded-[var(--radius-card)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-4 py-3 text-ui text-[var(--color-red)]"
					>
						<span>{error}</span>
						<button
							onclick={fetchTeam}
							class="shrink-0 font-semibold underline-offset-2 hover:underline"
						>
							Try again
						</button>
					</div>
				{/if}

				{#if loading}
					<div class="flex items-center justify-center py-24">
						<div class="flex items-center gap-3 text-ui text-[var(--color-text-secondary)]">
							<svg
								class="animate-spin"
								width="16"
								height="16"
								viewBox="0 0 24 24"
								fill="none"
								stroke="currentColor"
								stroke-width="2"
							>
								<path d="M21 12a9 9 0 1 1-6.219-8.56" />
							</svg>
							Loading team...
						</div>
					</div>
				{:else if !auth.teamId}
					<div class="flex flex-col items-center justify-center py-[72px]">
						<div class="mb-5 flex h-14 w-14 items-center justify-center rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-3)]">
							<svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="var(--color-text-secondary)" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
								<path d="M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/>
								<path d="M23 21v-2a4 4 0 0 0-3-3.87"/><path d="M16 3.13a4 4 0 0 1 0 7.75"/>
							</svg>
						</div>
						<p class="font-serif text-heading tracking-[-0.02em] text-[var(--color-text-bright)]">No team yet</p>
						<p class="mt-1.5 max-w-xs text-center text-ui text-[var(--color-text-tertiary)]">
							Use the team switcher in the sidebar to create your first team.
						</p>
					</div>
				{:else if team}
					<!-- ── Team Info ── -->
					<section class="mb-8">
						<div
							class="rounded-[var(--radius-card)] border border-[var(--color-border)] bg-[var(--color-bg-1)]"
						>
							<!-- Name row -->
							<div class="flex items-center gap-4 border-b border-[var(--color-border)] px-5 py-4">
								<div class="min-w-0 flex-1">
									<div
										class="mb-1.5 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]"
									>
										Team name
									</div>
									{#if editingName}
										<div class="flex items-center gap-2">
											<input
												bind:this={nameInputEl}
												bind:value={editName}
												onkeydown={(e) => {
													if (e.key === 'Enter' && !savingName) saveEditName();
													if (e.key === 'Escape' && !savingName) cancelEditName();
												}}
												disabled={savingName}
												class="min-w-0 flex-1 rounded-[var(--radius-input)] border border-[var(--color-accent)] bg-[var(--color-bg-4)] px-3 py-1.5 text-ui text-[var(--color-text-bright)] outline-none transition-colors duration-150 disabled:opacity-60"
											/>
											<!-- Save button -->
											<button
												onclick={saveEditName}
												disabled={savingName || !editName.trim()}
												title="Save"
												class="flex h-7 w-7 items-center justify-center rounded-[var(--radius-button)] border border-[var(--color-accent)]/40 bg-[var(--color-accent-glow-mid)] text-[var(--color-accent-bright)] transition-colors duration-150 hover:bg-[var(--color-accent-glow)] disabled:opacity-40"
											>
												{#if savingName}
													<svg
														class="animate-spin"
														width="12"
														height="12"
														viewBox="0 0 24 24"
														fill="none"
														stroke="currentColor"
														stroke-width="2"
													>
														<path d="M21 12a9 9 0 1 1-6.219-8.56" />
													</svg>
												{:else}
													<svg
														width="12"
														height="12"
														viewBox="0 0 24 24"
														fill="none"
														stroke="currentColor"
														stroke-width="2.5"
														stroke-linecap="round"
														stroke-linejoin="round"
													>
														<polyline points="20 6 9 17 4 12" />
													</svg>
												{/if}
											</button>
											<!-- Cancel button -->
											<button
												onclick={cancelEditName}
												disabled={savingName}
												title="Cancel"
												class="flex h-7 w-7 items-center justify-center rounded-[var(--radius-button)] border border-[var(--color-border-mid)] text-[var(--color-text-tertiary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-secondary)] disabled:opacity-40"
											>
												<svg
													width="12"
													height="12"
													viewBox="0 0 24 24"
													fill="none"
													stroke="currentColor"
													stroke-width="2.5"
													stroke-linecap="round"
													stroke-linejoin="round"
												>
													<line x1="18" y1="6" x2="6" y2="18" />
													<line x1="6" y1="6" x2="18" y2="18" />
												</svg>
											</button>
										</div>
										{#if nameError}
											<p class="mt-1.5 text-meta text-[var(--color-red)]">{nameError}</p>
										{/if}
									{:else}
										<!-- svelte-ignore a11y_click_events_have_key_events -->
										<div
											class="group flex items-center gap-2"
											onclick={canManage ? startEditName : undefined}
											role={canManage ? 'button' : undefined}
											tabindex={canManage ? 0 : undefined}
											onkeydown={canManage
												? (e) => {
														if (e.key === 'Enter' || e.key === ' ') startEditName();
													}
												: undefined}
											class:cursor-pointer={canManage}
											title={canManage ? 'Click to edit' : undefined}
										>
											<span
												class="text-ui font-medium transition-colors duration-300 {nameSavedFlash ? 'text-[var(--color-accent-mid)]' : 'text-[var(--color-text-bright)]'} {canManage ? 'group-hover:text-[var(--color-text-bright)]' : ''}"
											>
												{team.name}
											</span>
											{#if canManage}
												<svg
													width="12"
													height="12"
													viewBox="0 0 24 24"
													fill="none"
													stroke="currentColor"
													stroke-width="2"
													stroke-linecap="round"
													stroke-linejoin="round"
													class="shrink-0 text-[var(--color-text-tertiary)]"
												>
													<path
														d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"
													/>
													<path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z" />
												</svg>
											{/if}
										</div>
									{/if}
								</div>
							</div>

							<!-- Team ID -->
							<div class="flex items-center gap-3 px-5 py-4">
								<div class="min-w-0 flex-1">
									<div
										class="mb-1 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]"
									>
										Team ID
									</div>
									<span class="block truncate font-mono text-ui text-[var(--color-text-secondary)]"
										>{team.id}</span
									>
								</div>
								<button
									onclick={() => copyToClipboard(team!.id)}
									title="Copy team ID"
									class="flex shrink-0 items-center gap-1.5 rounded-[var(--radius-button)] border px-3 py-1.5 text-meta font-semibold transition-all duration-150
										{copiedId
										? 'border-[var(--color-accent)]/40 bg-[var(--color-accent-glow-mid)] text-[var(--color-accent-mid)]'
										: 'border-[var(--color-border-mid)] text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]'}"
								>
									{#if copiedId}
										<svg
											width="12"
											height="12"
											viewBox="0 0 24 24"
											fill="none"
											stroke="currentColor"
											stroke-width="2.5"
											stroke-linecap="round"
											stroke-linejoin="round"
											class="checkmark-draw"
										>
											<polyline points="20 6 9 17 4 12" />
										</svg>
										Copied
									{:else}
										<svg
											width="12"
											height="12"
											viewBox="0 0 24 24"
											fill="none"
											stroke="currentColor"
											stroke-width="2"
											stroke-linecap="round"
											stroke-linejoin="round"
										>
											<rect x="9" y="9" width="13" height="13" rx="2" ry="2" />
											<path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1" />
										</svg>
										Copy
									{/if}
								</button>
							</div>
						</div>
					</section>

					<!-- ── Members ── -->
					<section class="mb-8">
						<div class="mb-4 flex items-center justify-between">
							<div>
								<h2
									class="font-serif text-heading tracking-[-0.02em] text-[var(--color-text-bright)]"
								>
									Members
								</h2>
								<p class="mt-0.5 text-meta text-[var(--color-text-tertiary)]">
									{members.length}
									{members.length === 1 ? 'member' : 'members'}
								</p>
							</div>
							{#if canManage}
								<button
									onclick={() => {
										showAddMember = true;
										addEmail = '';
										searchResults = [];
										showResults = false;
										addError = null;
									}}
									class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-accent)] px-4 py-2 text-ui font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0"
								>
									<svg
										width="13"
										height="13"
										viewBox="0 0 24 24"
										fill="none"
										stroke="currentColor"
										stroke-width="2.5"
										stroke-linecap="round"
										stroke-linejoin="round"
									>
										<line x1="12" y1="5" x2="12" y2="19" />
										<line x1="5" y1="12" x2="19" y2="12" />
									</svg>
									Add Member
								</button>
							{/if}
						</div>

						<div
							class="overflow-hidden rounded-[var(--radius-card)] border border-[var(--color-border)]"
						>
							<!-- Table header -->
							<div
								class="grid grid-cols-[1fr_1fr_120px_140px_120px] border-b border-[var(--color-border)] bg-[var(--color-bg-3)]"
							>
								<div
									class="px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]"
								>
									Name
								</div>
								<div
									class="px-4 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]"
								>
									Email
								</div>
								<div
									class="px-4 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]"
								>
									Role
								</div>
								<div
									class="px-4 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]"
								>
									Joined
								</div>
								<div
									class="px-4 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]"
								></div>
							</div>

							{#each members as member, i (member.user_id)}
								<div
									class="grid grid-cols-[1fr_1fr_120px_140px_120px] items-center border-b border-[var(--color-border)] transition-colors duration-150 hover:bg-[var(--color-bg-3)] last:border-b-0 {recentlyAddedId === member.user_id ? 'member-flash' : ''}"
									in:fly={{ y: 6, duration: 200, delay: i * 30, easing: cubicOut }}
									out:fly={{ x: -16, duration: 180, easing: cubicOut }}
								>
									<!-- Name -->
									<div class="min-w-0 px-5 py-4">
										<div class="flex min-w-0 items-center gap-2">
											<span class="truncate text-ui text-[var(--color-text-bright)]"
												>{member.name || member.email}</span
											>
											{#if member.user_id === auth.userId}
												<span class="shrink-0 rounded-[2px] bg-[var(--color-accent-glow-mid)] px-1.5 py-0.5 text-badge font-semibold uppercase tracking-[0.05em] text-[var(--color-accent-mid)]">you</span>
											{/if}
										</div>
									</div>

									<!-- Email -->
									<div class="min-w-0 px-4 py-4">
										<span class="truncate font-mono text-ui text-[var(--color-text-secondary)]">{member.email}</span>
									</div>

									<!-- Role badge -->
									<div class="px-4 py-4">
										{#if updatingRoleId === member.user_id}
											<svg
												class="animate-spin text-[var(--color-text-tertiary)]"
												width="14"
												height="14"
												viewBox="0 0 24 24"
												fill="none"
												stroke="currentColor"
												stroke-width="2"
											>
												<path d="M21 12a9 9 0 1 1-6.219-8.56" />
											</svg>
										{:else}
											<span
												class="inline-flex items-center rounded-[3px] px-2 py-0.5 text-badge font-semibold uppercase tracking-[0.06em]
													{member.role === 'owner'
													? 'bg-[var(--color-accent-glow-mid)] text-[var(--color-accent-mid)]'
													: member.role === 'admin'
														? 'bg-[var(--color-amber)]/8 text-[var(--color-amber)]'
														: 'bg-[var(--color-bg-4)] text-[var(--color-text-muted)]'}"
											>
												{roleLabel(member.role)}
											</span>
										{/if}
									</div>

									<!-- Joined date -->
									<div class="px-4 py-4">
										<span class="text-ui text-[var(--color-text-secondary)]"
											>{formatDate(member.joined_at)}</span
										>
									</div>

									<!-- Actions: split button -->
									<div class="flex items-center justify-end px-3 py-3">
										{#if canActOn(member)}
											<div
												class="split-btn-container relative flex items-stretch overflow-hidden rounded-[var(--radius-button)] border border-[var(--color-border-mid)] bg-[var(--color-bg-3)]"
											>
												<!-- Primary: Remove -->
												<button
													onclick={() => {
														removeTarget = member;
														removeError = null;
													}}
													class="flex items-center px-3 py-1.5 text-meta font-medium text-[var(--color-text-primary)] transition-colors duration-150 hover:bg-[var(--color-bg-4)] hover:text-[var(--color-red)]"
												>
													Remove
												</button>
												<!-- Divider -->
												<div class="w-px shrink-0 bg-[var(--color-border-mid)]"></div>
												<!-- Chevron: Make Admin / Make Member -->
												<button
													onclick={(e) => {
														e.stopPropagation();
														if (openDropdownId === member.user_id) {
															openDropdownId = null;
														} else {
															const rect = (
																e.currentTarget as HTMLElement
															).getBoundingClientRect();
															dropdownPos = {
																top: rect.bottom + 4,
																left: rect.right - 140
															};
															openDropdownId = member.user_id;
														}
													}}
													class="flex items-center px-2 py-1.5 text-[var(--color-text-secondary)] transition-colors duration-150 hover:bg-[var(--color-bg-4)] hover:text-[var(--color-text-bright)]"
												>
													<svg
														class="transition-transform duration-150 {openDropdownId === member.user_id
															? 'rotate-180'
															: ''}"
														width="12"
														height="12"
														viewBox="0 0 24 24"
														fill="none"
														stroke="currentColor"
														stroke-width="2.5"
														stroke-linecap="round"
														stroke-linejoin="round"
													>
														<polyline points="6 9 12 15 18 9" />
													</svg>
												</button>
											</div>
										{/if}
									</div>
								</div>
							{/each}
						</div>
					</section>

					<!-- ── Danger Zone ── -->
					<section>
						<div
							class="rounded-[var(--radius-card)] border border-[var(--color-red)]/25 bg-[var(--color-red)]/[0.03]"
						>
							<div class="border-b border-[var(--color-red)]/15 px-5 py-4">
								<h2
									class="font-serif text-heading tracking-[-0.02em] text-[var(--color-text-bright)]"
								>
									Danger Zone
								</h2>
							</div>
							<div class="flex items-center justify-between gap-6 px-5 py-4">
								{#if myRole === 'owner'}
									<div>
										<p class="text-ui font-medium text-[var(--color-text-primary)]">
											Delete this team
										</p>
										<p class="mt-0.5 text-meta text-[var(--color-text-tertiary)]">
											{#if isLastTeam}Create another team first — you can't delete your only team.{:else}Permanently deletes the team and destroys all running capsules. This cannot be undone.{/if}
										</p>
									</div>
									<button
										onclick={() => { showDangerConfirm = true; dangerError = null; }}
										disabled={isLastTeam}
										class="shrink-0 rounded-[var(--radius-button)] border px-4 py-2 text-ui font-semibold transition-all duration-150 {isLastTeam ? 'cursor-not-allowed border-[var(--color-border)] text-[var(--color-text-muted)] opacity-50' : 'border-[var(--color-red)]/40 text-[var(--color-red)] hover:bg-[var(--color-red)]/10 hover:border-[var(--color-red)]/60'}"
									>
										Delete Team
									</button>
								{:else}
									<div>
										<p class="text-ui font-medium text-[var(--color-text-primary)]">
											Leave this team
										</p>
										<p class="mt-0.5 text-meta text-[var(--color-text-tertiary)]">
											{#if isLastTeam}Create another team first — you can't leave your only team.{:else}You'll immediately lose access to all capsules and resources in this team.{/if}
										</p>
									</div>
									<button
										onclick={() => { showDangerConfirm = true; dangerError = null; }}
										disabled={isLastTeam}
										class="shrink-0 rounded-[var(--radius-button)] border px-4 py-2 text-ui font-semibold transition-all duration-150 {isLastTeam ? 'cursor-not-allowed border-[var(--color-border)] text-[var(--color-text-muted)] opacity-50' : 'border-[var(--color-red)]/40 text-[var(--color-red)] hover:bg-[var(--color-red)]/10 hover:border-[var(--color-red)]/60'}"
									>
										Leave Team
									</button>
								{/if}
							</div>
						</div>
					</section>
				{/if}
			</div>
		</main>

		<footer class="h-px shrink-0 bg-[var(--color-border)]"></footer>
	</div>
</div>

<!-- Split button dropdown -->
{#if openDropdownId}
	{@const dropdownMember = members.find((m) => m.user_id === openDropdownId)}
	{#if dropdownMember}
		<div
			class="fixed z-50 w-36 overflow-hidden rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] py-1"
			style="top: {dropdownPos.top}px; left: {dropdownPos.left}px; animation: fadeUp 0.15s ease both"
		>
			{#if dropdownMember.role === 'member'}
				<button
					onclick={(e) => {
						e.stopPropagation();
						handleUpdateRole(dropdownMember, 'admin');
					}}
					class="flex w-full items-center gap-2 px-3 py-2 text-meta text-[var(--color-text-primary)] transition-colors duration-150 hover:bg-[var(--color-bg-3)]"
				>
					<svg
						width="13"
						height="13"
						viewBox="0 0 24 24"
						fill="none"
						stroke="currentColor"
						stroke-width="2"
						stroke-linecap="round"
						stroke-linejoin="round"
						class="shrink-0 text-[var(--color-text-secondary)]"
					>
						<path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z" />
					</svg>
					Make Admin
				</button>
			{:else if dropdownMember.role === 'admin'}
				<button
					onclick={(e) => {
						e.stopPropagation();
						handleUpdateRole(dropdownMember, 'member');
					}}
					class="flex w-full items-center gap-2 px-3 py-2 text-meta text-[var(--color-text-primary)] transition-colors duration-150 hover:bg-[var(--color-bg-3)]"
				>
					<svg
						width="13"
						height="13"
						viewBox="0 0 24 24"
						fill="none"
						stroke="currentColor"
						stroke-width="2"
						stroke-linecap="round"
						stroke-linejoin="round"
						class="shrink-0 text-[var(--color-text-secondary)]"
					>
						<path d="M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2" />
						<circle cx="12" cy="7" r="4" />
					</svg>
					Make Member
				</button>
			{/if}
		</div>
	{/if}
{/if}

<!-- Add Member Dialog -->
{#if showAddMember}
	<div class="fixed inset-0 z-50 flex items-center justify-center">
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="absolute inset-0 bg-black/60"
			onclick={() => {
				if (!adding) {
					showAddMember = false;
				}
			}}
			onkeydown={(e) => {
				if (e.key === 'Escape' && !adding) showAddMember = false;
			}}
		></div>

		<div
			class="relative w-full max-w-[400px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] p-6"
			style="animation: fadeUp 0.2s ease both"
		>
			<h2
				class="font-serif text-heading tracking-[-0.02em] text-[var(--color-text-bright)]"
			>
				Add Member
			</h2>
			<p class="mt-1 text-ui text-[var(--color-text-tertiary)]">
				Search by email. The user must already have a Wrenn account.
			</p>

			{#if addError}
				<div
					class="mt-4 rounded-[var(--radius-input)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-3 py-2 text-meta text-[var(--color-red)]"
				>
					{addError}
				</div>
			{/if}

			<div class="mt-5">
				<label
					class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]"
					for="add-email"
				>
					Email address
				</label>
				<div class="search-container relative">
					<div class="relative">
						<input
							id="add-email"
							type="email"
							placeholder="colleague@example.com"
							bind:value={addEmail}
							oninput={handleSearchInput}
							onkeydown={(e) => {
								if (e.key === 'Enter' && !adding) handleAddMember();
							}}
							disabled={adding}
							class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-4)] px-3 py-2 text-ui text-[var(--color-text-bright)] outline-none placeholder:text-[var(--color-text-muted)] transition-colors duration-150 focus:border-[var(--color-accent)] disabled:opacity-60"
						/>
						{#if searchLoading}
							<div class="absolute right-2.5 top-1/2 -translate-y-1/2">
								<svg
									class="animate-spin text-[var(--color-text-tertiary)]"
									width="14"
									height="14"
									viewBox="0 0 24 24"
									fill="none"
									stroke="currentColor"
									stroke-width="2"
								>
									<path d="M21 12a9 9 0 1 1-6.219-8.56" />
								</svg>
							</div>
						{/if}
					</div>

					<!-- Typeahead results -->
					{#if showResults && searchResults.length > 0}
						<div
							class="absolute left-0 right-0 top-full z-10 mt-1 overflow-hidden rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] py-1"
							style="animation: fadeUp 0.15s ease both"
						>
							{#each searchResults as result (result.user_id)}
								<button
									onclick={() => selectSearchResult(result)}
									class="flex w-full items-center gap-2.5 px-3 py-2 text-ui transition-colors duration-150 hover:bg-[var(--color-bg-3)]"
								>
									<div
										class="flex h-5 w-5 shrink-0 items-center justify-center rounded-full text-badge font-bold uppercase"
										style="background: {avatarColor(result.email)}22; color: {avatarColor(result.email)}"
									>
										{result.email[0]}
									</div>
									<span class="min-w-0 truncate text-[var(--color-text-primary)]"
										>{result.email}</span
									>
								</button>
							{/each}
						</div>
					{/if}
				</div>
			</div>

			<div class="mt-6 flex justify-end gap-3">
				<button
					onclick={() => {
						showAddMember = false;
					}}
					disabled={adding}
					class="rounded-[var(--radius-button)] border border-[var(--color-border)] px-4 py-2 text-ui text-[var(--color-text-secondary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)] disabled:opacity-50"
				>
					Cancel
				</button>
				<button
					onclick={handleAddMember}
					disabled={adding || !addEmail.trim()}
					class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-accent)] px-5 py-2 text-ui font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0 disabled:opacity-50 disabled:hover:translate-y-0"
				>
					{#if adding}
						<svg
							class="animate-spin"
							width="13"
							height="13"
							viewBox="0 0 24 24"
							fill="none"
							stroke="currentColor"
							stroke-width="2"
						>
							<path d="M21 12a9 9 0 1 1-6.219-8.56" />
						</svg>
						Adding...
					{:else}
						Add Member
					{/if}
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Remove Member Confirmation Dialog -->
{#if removeTarget}
	<div class="fixed inset-0 z-50 flex items-center justify-center">
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="absolute inset-0 bg-black/60"
			onclick={() => {
				if (!removing) removeTarget = null;
			}}
			onkeydown={(e) => {
				if (e.key === 'Escape' && !removing) removeTarget = null;
			}}
		></div>

		<div
			class="relative w-full max-w-[380px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] p-6"
			style="animation: fadeUp 0.2s ease both"
		>
			<h2
				class="font-serif text-heading tracking-[-0.02em] text-[var(--color-text-bright)]"
			>
				Remove Member
			</h2>
			<p class="mt-2 text-ui text-[var(--color-text-tertiary)]">
				<span class="font-medium text-[var(--color-text-secondary)]"
					>{removeTarget.name || removeTarget.email}</span
				> will immediately lose access to all team capsules and resources.
			</p>

			{#if removeError}
				<div
					class="mt-4 rounded-[var(--radius-input)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-3 py-2 text-meta text-[var(--color-red)]"
				>
					{removeError}
				</div>
			{/if}

			<div class="mt-6 flex justify-end gap-3">
				<button
					onclick={() => {
						removeTarget = null;
					}}
					disabled={removing}
					class="rounded-[var(--radius-button)] border border-[var(--color-border)] px-4 py-2 text-ui text-[var(--color-text-secondary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)] disabled:opacity-50"
				>
					Cancel
				</button>
				<button
					onclick={handleRemoveMember}
					disabled={removing}
					class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-red)] px-5 py-2 text-ui font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0 disabled:opacity-50 disabled:hover:translate-y-0"
				>
					{#if removing}
						<svg
							class="animate-spin"
							width="13"
							height="13"
							viewBox="0 0 24 24"
							fill="none"
							stroke="currentColor"
							stroke-width="2"
						>
							<path d="M21 12a9 9 0 1 1-6.219-8.56" />
						</svg>
						Removing...
					{:else}
						Remove
					{/if}
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Danger Zone Confirmation Dialog -->
{#if showDangerConfirm}
	<div class="fixed inset-0 z-50 flex items-center justify-center">
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="absolute inset-0 bg-black/60"
			onclick={() => {
				if (!dangerLoading) showDangerConfirm = false;
			}}
			onkeydown={(e) => {
				if (e.key === 'Escape' && !dangerLoading) showDangerConfirm = false;
			}}
		></div>

		<div
			class="relative w-full max-w-[400px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] p-6"
			style="animation: fadeUp 0.2s ease both"
		>
			{#if myRole === 'owner'}
				<h2
					class="font-serif text-heading tracking-[-0.02em] text-[var(--color-text-bright)]"
				>
					Delete Team
				</h2>
				<p class="mt-2 text-ui text-[var(--color-text-tertiary)]">
					This will permanently delete <span class="font-medium text-[var(--color-text-secondary)]"
						>{team?.name}</span
					> and destroy all running capsules. This action cannot be undone.
				</p>
			{:else}
				<h2
					class="font-serif text-heading tracking-[-0.02em] text-[var(--color-text-bright)]"
				>
					Leave Team
				</h2>
				<p class="mt-2 text-ui text-[var(--color-text-tertiary)]">
					You'll immediately lose access to all capsules and resources in <span
						class="font-medium text-[var(--color-text-secondary)]">{team?.name}</span
					>.
				</p>
			{/if}

			<!-- Amber warning -->
			<div
				class="mt-4 flex items-start gap-2 rounded-[var(--radius-input)] border border-[var(--color-amber)]/20 bg-[var(--color-amber)]/5 px-3 py-2.5"
			>
				<svg
					class="mt-0.5 shrink-0"
					width="13"
					height="13"
					viewBox="0 0 24 24"
					fill="none"
					stroke="var(--color-amber)"
					stroke-width="2"
					stroke-linecap="round"
					stroke-linejoin="round"
				>
					<path
						d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"
					/>
					<line x1="12" y1="9" x2="12" y2="13" />
					<line x1="12" y1="17" x2="12.01" y2="17" />
				</svg>
				<p class="text-meta leading-relaxed text-[var(--color-amber)]">
					{#if myRole === 'owner'}
						All team data, API keys, and running capsules will be permanently destroyed.
					{:else}
						You'll need a new invitation to rejoin this team.
					{/if}
				</p>
			</div>

			{#if dangerError}
				<div
					class="mt-4 rounded-[var(--radius-input)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-3 py-2 text-meta text-[var(--color-red)]"
				>
					{dangerError}
				</div>
			{/if}

			<div class="mt-6 flex justify-end gap-3">
				<button
					onclick={() => {
						showDangerConfirm = false;
					}}
					disabled={dangerLoading}
					class="rounded-[var(--radius-button)] border border-[var(--color-border)] px-4 py-2 text-ui text-[var(--color-text-secondary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)] disabled:opacity-50"
				>
					Cancel
				</button>
				<button
					onclick={handleDangerAction}
					disabled={dangerLoading}
					class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-red)] px-5 py-2 text-ui font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0 disabled:opacity-50 disabled:hover:translate-y-0"
				>
					{#if dangerLoading}
						<svg
							class="animate-spin"
							width="13"
							height="13"
							viewBox="0 0 24 24"
							fill="none"
							stroke="currentColor"
							stroke-width="2"
						>
							<path d="M21 12a9 9 0 1 1-6.219-8.56" />
						</svg>
					{/if}
					{myRole === 'owner' ? 'Delete Team' : 'Leave Team'}
				</button>
			</div>
		</div>
	</div>
{/if}

<style>
	/* Checkmark SVG path draw animation for copy buttons */
	.checkmark-draw polyline {
		stroke-dasharray: 24;
		stroke-dashoffset: 24;
		animation: draw-check 0.25s cubic-bezier(0.4, 0, 0.2, 1) forwards;
	}
	@keyframes draw-check {
		to {
			stroke-dashoffset: 0;
		}
	}

	/* New member row entrance flash */
	.member-flash {
		animation: member-added 1.2s ease forwards;
	}
	@keyframes member-added {
		0%   { background-color: transparent; }
		15%  { background-color: var(--color-accent-glow-mid); }
		100% { background-color: transparent; }
	}
</style>
