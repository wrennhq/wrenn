<script lang="ts">
	import { page } from '$app/stores';
	import { onMount } from 'svelte';
	import { Popover } from 'bits-ui';
	import { auth } from '$lib/auth.svelte';
	import { teams as teamsStore } from '$lib/teams.svelte';
	import { createTeam, switchTeam } from '$lib/api/team';
	import {
		IconMonitor,
		IconBox,
		IconKey,
		IconMembers,
		IconUsage,
		IconBilling,
		IconSettings,
		IconLogout,
		IconChevron,
		IconPlus,
		IconSidebar,
		IconBell,
		IconDocs,
		IconAudit,
		IconServer,
		IconShield,
		IconMetrics,
		IconBroadcast
	} from './icons';

	let { collapsed = $bindable(false) }: { collapsed: boolean } = $props();

	let teamPopoverOpen = $state(false);

	let currentTeamName = $derived(teamsStore.list.find((t) => t.id === auth.teamId)?.name ?? '');
	let userName = $derived(auth.name || auth.email || '');

	// Create team dialog
	let showCreateTeam = $state(false);
	let newTeamName = $state('');
	let creatingTeam = $state(false);
	let createTeamError = $state<string | null>(null);

	type NavItem = {
		label: string;
		icon: typeof IconMonitor;
		href: string;
		disabled?: boolean;
		disabledHint?: string;
	};

	const platformItems: NavItem[] = [
		{ label: 'Capsules', icon: IconMonitor, href: '/dashboard/capsules' },
		{ label: 'Templates', icon: IconBox, href: '/dashboard/templates' },
		{ label: 'Metrics', icon: IconMetrics, href: '/dashboard/metrics' }
	];

	let currentTeamIsByoc = $derived(
		teamsStore.list.find((t) => t.id === auth.teamId)?.is_byoc ?? false
	);

	let managementItems = $derived<NavItem[]>([
		{ label: 'Keys', icon: IconKey, href: '/dashboard/keys' },
		{ label: 'Channels', icon: IconBroadcast, href: '/dashboard/channels' },
		{ label: 'Team', icon: IconMembers, href: '/dashboard/team' },
		{ label: 'Audit Logs', icon: IconAudit, href: '/dashboard/audit' },
		...(currentTeamIsByoc
			? [{
					label: 'Hosts',
					icon: IconServer,
					href: '/dashboard/hosts',
					disabled: auth.role === 'member',
					disabledHint: 'Available to team owners and admins only'
				}]
			: [])
	]);

	const billingItems: NavItem[] = [
		{ label: 'Usage', icon: IconUsage, href: '/dashboard/usage' },
		{ label: 'Billing', icon: IconBilling, href: '/dashboard/billing' }
	];

	function isActive(href: string): boolean {
		const p = $page.url.pathname;
		return p === href || p.startsWith(href + '/');
	}

	function toggleCollapsed() {
		collapsed = !collapsed;
		localStorage.setItem('wrenn_sidebar_collapsed', String(collapsed));
	}

	async function fetchTeams() {
		await teamsStore.fetch();
	}

	async function handleSwitchTeam(teamId: string) {
		if (teamId === auth.teamId) {
			teamPopoverOpen = false;
			return;
		}
		teamPopoverOpen = false;
		const result = await switchTeam(teamId);
		if (result.ok) {
			auth.login(result.data);
			window.location.reload();
		}
	}

	async function handleCreateTeam() {
		if (!newTeamName.trim()) return;
		creatingTeam = true;
		createTeamError = null;
		const result = await createTeam(newTeamName.trim());
		if (result.ok) {
			const switchResult = await switchTeam(result.data.id);
			if (switchResult.ok) {
				auth.login(switchResult.data);
				window.location.reload();
			} else {
				createTeamError = switchResult.error;
				creatingTeam = false;
			}
		} else {
			createTeamError = result.error;
			creatingTeam = false;
		}
	}

	onMount(fetchTeams);
</script>

<aside
	class="flex h-screen shrink-0 flex-col overflow-hidden border-r border-[var(--color-border)] bg-[var(--color-bg-1)] transition-[width] duration-250 ease-in-out"
	style="width: {collapsed ? '56px' : '230px'}"
>
	<!-- Brand + collapse toggle -->
	<div class="flex shrink-0 items-center px-4 pt-5 pb-4 {collapsed ? 'justify-center' : 'justify-between'}">
		{#if !collapsed}
			<div class="flex items-center gap-2.5">
				<img
					src="/logo.svg"
					alt="Wrenn"
					class="h-7 w-7 shrink-0 rounded-[var(--radius-logo)]"
				/>
				<span class="font-brand text-[1.286rem] text-[var(--color-text-bright)]">Wrenn</span>
			</div>
		{/if}
		<button
			onclick={toggleCollapsed}
			class="flex h-7 w-7 shrink-0 items-center justify-center rounded-[var(--radius-button)] text-[var(--color-text-tertiary)] transition-colors duration-150 hover:bg-[var(--color-bg-3)] hover:text-[var(--color-text-secondary)]"
			title={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
		>
			<IconSidebar size={16} />
		</button>
	</div>

	<!-- Team switcher -->
	<div class="px-3 pb-0 {collapsed ? 'px-1.5' : ''}">
		<Popover.Root bind:open={teamPopoverOpen}>
			<Popover.Trigger
				class="flex w-full items-center rounded-[var(--radius-input)] py-2 text-left transition-colors duration-150 hover:bg-[var(--color-bg-3)] {collapsed
					? 'justify-center px-0'
					: 'gap-2 px-2.5'}"
			>
				<div
					class="flex h-6 w-6 shrink-0 items-center justify-center rounded-[var(--radius-avatar)] bg-[var(--color-bg-4)] text-badge font-bold uppercase text-[var(--color-text-secondary)]"
				>
					{(currentTeamName || '?')[0].toUpperCase()}
				</div>
				{#if !collapsed}
					<div class="min-w-0 flex-1 overflow-hidden whitespace-nowrap">
						<div
							class="text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]"
						>
							Team
						</div>
						<div class="truncate text-ui text-[var(--color-text-primary)]">
							{currentTeamName || '…'}
						</div>
					</div>
					<IconChevron
						size={12}
						direction="down"
						class="shrink-0 text-[var(--color-text-tertiary)]"
					/>
				{/if}
			</Popover.Trigger>
			<Popover.Portal>
				<Popover.Content
					side="bottom"
					align="start"
					sideOffset={4}
					class="z-50 w-[210px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] p-1.5"
					style="animation: popoverSlideIn 150ms ease"
				>
					<div
						class="mb-1 px-2.5 py-1 text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]"
					>
						Teams
					</div>
					{#each teamsStore.list as team (team.id)}
						<button
							class="flex w-full items-center gap-2.5 rounded-[var(--radius-input)] px-2.5 py-2 text-ui transition-colors duration-150 hover:bg-[var(--color-bg-3)] {team.id ===
							auth.teamId
								? 'bg-[var(--color-accent-glow)]'
								: ''}"
							onclick={() => handleSwitchTeam(team.id)}
						>
							<div
								class="flex h-5 w-5 items-center justify-center rounded-[var(--radius-avatar)] text-badge font-bold uppercase text-white {team.id ===
								auth.teamId
									? 'bg-[var(--color-accent)]'
									: 'bg-[var(--color-bg-5)]'}"
							>
								{team.name[0].toUpperCase()}
							</div>
							<span
								class={team.id === auth.teamId
									? 'font-medium text-[var(--color-text-bright)]'
									: 'text-[var(--color-text-primary)]'}
							>
								{team.name}
							</span>
						</button>
					{/each}
					<div class="mt-0.5 border-t border-[var(--color-border)] pt-0.5">
						<button
							onclick={() => {
								teamPopoverOpen = false;
								newTeamName = '';
								createTeamError = null;
								showCreateTeam = true;
							}}
							class="flex w-full items-center gap-2.5 rounded-[var(--radius-input)] px-2.5 py-2 text-ui text-[var(--color-text-secondary)] transition-colors duration-150 hover:bg-[var(--color-bg-3)] hover:text-[var(--color-text-primary)]"
						>
							<IconPlus size={14} />
							Create team
						</button>
					</div>
				</Popover.Content>
			</Popover.Portal>
		</Popover.Root>
	</div>

	<!-- Divider after team switcher -->
	<div class="mx-4 mb-3 h-px bg-[var(--color-border)] {collapsed ? 'mx-3' : ''}"></div>

	<!-- Navigation -->
	<nav class="flex-1 overflow-y-auto px-3 {collapsed ? 'px-1.5' : ''}">
		{@render navSection('Platform', platformItems)}
		{@render navSection('Management', managementItems)}
		{@render navSection('Billing', billingItems)}
	</nav>

	<!-- Bottom links -->
	<div class="shrink-0 px-3 pb-1 {collapsed ? 'px-1.5' : ''}">
		{#if auth.isAdmin}
			<a
				href="/admin"
				class="group flex items-center rounded-[var(--radius-input)] px-2.5 py-2.5 text-[var(--color-text-secondary)] transition-colors duration-150 hover:bg-[var(--color-bg-3)] hover:text-[var(--color-text-primary)] {collapsed ? 'justify-center px-2' : 'gap-3'}"
				title={collapsed ? 'Admin' : undefined}
			>
				<IconShield size={16} class="shrink-0 opacity-50 transition-opacity duration-150 group-hover:opacity-100" />
				{#if !collapsed}<span class="text-ui">Admin</span>{/if}
			</a>
		{/if}
		<a
			href="https://docs.wrenn.dev"
			target="_blank"
			rel="noopener noreferrer"
			class="group flex items-center rounded-[var(--radius-input)] px-2.5 py-2.5 text-[var(--color-text-secondary)] transition-colors duration-150 hover:bg-[var(--color-bg-3)] hover:text-[var(--color-text-primary)] {collapsed ? 'justify-center px-2' : 'gap-3'}"
			title={collapsed ? 'Docs' : undefined}
		>
			<IconDocs size={16} class="shrink-0 opacity-50 transition-opacity duration-150 group-hover:opacity-100" />
			{#if !collapsed}<span class="text-ui">Docs</span>{/if}
		</a>
		<div
			class="flex cursor-not-allowed items-center rounded-[var(--radius-input)] px-2.5 py-2.5 opacity-35 {collapsed ? 'justify-center px-2' : 'gap-3'}"
			title={collapsed ? 'Notifications (coming soon)' : 'Coming soon'}
		>
			<IconBell size={16} class="shrink-0" />
			{#if !collapsed}<span class="text-ui">Notifications</span>{/if}
		</div>
		<a
			href="/dashboard/settings"
			class="group relative flex items-center rounded-[var(--radius-input)] px-2.5 py-2.5 transition-colors duration-150 hover:bg-[var(--color-bg-3)] {collapsed ? 'justify-center px-2' : 'gap-3'} {isActive('/dashboard/settings') ? (collapsed ? 'bg-[var(--color-accent-glow-mid)]' : 'bg-[var(--color-accent)]/[0.12]') : ''}"
			title={collapsed ? 'Settings' : undefined}
		>
			{#if isActive('/dashboard/settings') && !collapsed}
				<div class="absolute left-0 top-1/2 h-6 w-1 -translate-y-1/2 rounded-r-full bg-[var(--color-accent)]"></div>
			{/if}
			<IconSettings size={16} class="shrink-0 {isActive('/dashboard/settings') ? 'text-[var(--color-accent-bright)]' : 'opacity-50 transition-opacity duration-150 group-hover:opacity-100'}" />
			{#if !collapsed}
				<span class="text-ui transition-colors duration-150 {isActive('/dashboard/settings') ? 'font-semibold text-[var(--color-accent-bright)]' : 'text-[var(--color-text-primary)] group-hover:text-[var(--color-text-bright)]'}">
					Settings
				</span>
			{/if}
		</a>
	</div>

	<!-- User footer -->
	<div
		class="flex shrink-0 items-center border-t border-[var(--color-border)] px-3 py-2.5 {collapsed
			? 'justify-center px-1.5'
			: 'gap-2.5'}"
	>
		{#if !collapsed}
			<div
				class="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-[var(--color-bg-4)] text-badge font-bold uppercase text-[var(--color-text-secondary)]"
			>
				{userName[0] ?? ''}
			</div>
			<span class="flex-1 truncate text-ui text-[var(--color-text-secondary)]">
				{userName}
			</span>
		{/if}
		<button
			onclick={() => auth.logout()}
			class="flex shrink-0 items-center justify-center rounded-[var(--radius-button)] transition-colors duration-150 hover:text-[var(--color-red)] {collapsed
				? 'h-7 w-7 text-[var(--color-text-muted)] hover:bg-[var(--color-bg-3)]'
				: 'h-6 w-6 text-[var(--color-text-tertiary)]'}"
			title="Sign out"
		>
			<IconLogout size={collapsed ? 15 : 14} />
		</button>
	</div>
</aside>

{#snippet navSection(label: string, items: NavItem[])}
	<div class="mb-3">
		{#if collapsed}
			{#if label !== 'Platform'}
				<div class="mx-1 my-2 h-px bg-[var(--color-border)]"></div>
			{/if}
		{:else}
			<div
				class="mb-1 px-2.5 py-1.5 text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]"
			>
				{label}
			</div>
		{/if}
		{#each items as item}
			{#if item.disabled}
				<div
					class="flex cursor-not-allowed items-center rounded-[var(--radius-input)] px-2.5 py-2.5 opacity-35 {collapsed
						? 'justify-center px-2'
						: 'gap-3'}"
					title={collapsed ? item.disabledHint ?? item.label : item.disabledHint}
				>
					<item.icon size={16} class="shrink-0" />
					{#if !collapsed}
						<span class="text-ui text-[var(--color-text-primary)]">{item.label}</span>
					{/if}
				</div>
			{:else if isActive(item.href)}
				<a
					href={item.href}
					class="group relative flex items-center rounded-[var(--radius-input)] px-2.5 py-2.5 transition-colors duration-150 {collapsed
						? 'justify-center px-2 bg-[var(--color-accent-glow-mid)]'
						: 'gap-3 bg-[var(--color-accent)]/[0.12]'}"
					title={collapsed ? item.label : undefined}
				>
					{#if !collapsed}
						<div
							class="absolute left-0 top-1/2 h-6 w-1 -translate-y-1/2 rounded-r-full bg-[var(--color-accent)]"
						></div>
					{/if}
					<item.icon size={16} class="shrink-0 text-[var(--color-accent-bright)]" />
					{#if !collapsed}
						<span class="text-ui font-semibold text-[var(--color-accent-bright)]">
							{item.label}
						</span>
					{/if}
				</a>
			{:else}
				<a
					href={item.href}
					class="group flex items-center rounded-[var(--radius-input)] px-2.5 py-2.5 transition-colors duration-150 hover:bg-[var(--color-bg-3)] {collapsed
						? 'justify-center px-2'
						: 'gap-3'}"
					title={collapsed ? item.label : undefined}
				>
					<item.icon
						size={16}
						class="shrink-0 opacity-50 transition-opacity duration-150 group-hover:opacity-100"
					/>
					{#if !collapsed}
						<span
							class="text-ui text-[var(--color-text-primary)] transition-colors duration-150 group-hover:text-[var(--color-text-bright)]"
						>
							{item.label}
						</span>
					{/if}
				</a>
			{/if}
		{/each}
	</div>
{/snippet}

<!-- Create Team Dialog -->
{#if showCreateTeam}
	<div class="fixed inset-0 z-50 flex items-center justify-center">
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="absolute inset-0 bg-black/60"
			onclick={() => { if (!creatingTeam) showCreateTeam = false; }}
			onkeydown={(e) => { if (e.key === 'Escape' && !creatingTeam) showCreateTeam = false; }}
		></div>

		<div
			class="relative w-full max-w-[380px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] p-6"
			style="animation: fadeUp 0.2s ease both; box-shadow: var(--shadow-dialog)"
		>
			<h2 class="font-serif text-heading text-[var(--color-text-bright)]">
				Create Team
			</h2>
			<p class="mt-1 text-ui text-[var(--color-text-tertiary)]">
				Choose a name for your new team.
			</p>

			{#if createTeamError}
				<div
					class="mt-4 rounded-[var(--radius-input)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-3 py-2 text-meta text-[var(--color-red)]"
				>
					{createTeamError}
				</div>
			{/if}

			<div class="mt-5">
				<label
					class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]"
					for="new-team-name"
				>
					Team name
				</label>
				<input
					id="new-team-name"
					type="text"
					placeholder="e.g. Acme Engineering"
					bind:value={newTeamName}
					onkeydown={(e) => { if (e.key === 'Enter' && !creatingTeam) handleCreateTeam(); }}
					disabled={creatingTeam}
					class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-4)] px-3 py-2 text-ui text-[var(--color-text-bright)] outline-none placeholder:text-[var(--color-text-muted)] transition-colors duration-150 focus:border-[var(--color-accent)] disabled:opacity-60"
				/>
			</div>

			<div class="mt-6 flex justify-end gap-3">
				<button
					onclick={() => { showCreateTeam = false; }}
					disabled={creatingTeam}
					class="rounded-[var(--radius-button)] border border-[var(--color-border)] px-4 py-2 text-ui text-[var(--color-text-secondary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)] disabled:opacity-50"
				>
					Cancel
				</button>
				<button
					onclick={handleCreateTeam}
					disabled={creatingTeam || !newTeamName.trim()}
					class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-accent)] px-5 py-2 text-ui font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0 disabled:opacity-50 disabled:hover:translate-y-0"
				>
					{#if creatingTeam}
						<svg class="animate-spin" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
							<path d="M21 12a9 9 0 1 1-6.219-8.56" />
						</svg>
						Creating...
					{:else}
						Create Team
					{/if}
				</button>
			</div>
		</div>
	</div>
{/if}

<style>
	@keyframes popoverSlideIn {
		from {
			opacity: 0;
			transform: translateY(-4px);
		}
		to {
			opacity: 1;
			transform: translateY(0);
		}
	}
</style>
