<script lang="ts">
	import { page } from '$app/stores';
	import { Popover } from 'bits-ui';
	import { auth } from '$lib/auth.svelte';
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
		IconAudit
	} from './icons';

	let { collapsed = $bindable(false) }: { collapsed: boolean } = $props();

	let teamPopoverOpen = $state(false);

	const currentTeam = 'default';
	const userName = $derived(auth.email ?? '');

	type NavItem = {
		label: string;
		icon: typeof IconMonitor;
		href: string;
	};

	const platformItems: NavItem[] = [
		{ label: 'Capsules', icon: IconMonitor, href: '/dashboard/capsules' },
		{ label: 'Templates', icon: IconBox, href: '/dashboard/snapshots' }
	];

	const managementItems: NavItem[] = [
		{ label: 'Keys', icon: IconKey, href: '/dashboard/keys' },
		{ label: 'Members', icon: IconMembers, href: '/dashboard/members' },
		{ label: 'Audit Logs', icon: IconAudit, href: '/dashboard/audit' }
	];

	const billingItems: NavItem[] = [
		{ label: 'Usage', icon: IconUsage, href: '/dashboard/usage' },
		{ label: 'Billing', icon: IconBilling, href: '/dashboard/billing' }
	];

	const teams = ['default', 'Wrenn Labs', 'Acme Corp'];

	function isActive(href: string): boolean {
		const p = $page.url.pathname;
		return p === href || p.startsWith(href + '/');
	}

	function toggleCollapsed() {
		collapsed = !collapsed;
		localStorage.setItem('wrenn_sidebar_collapsed', String(collapsed));
	}
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
					{currentTeam[0]}
				</div>
				{#if !collapsed}
					<div class="min-w-0 flex-1 overflow-hidden whitespace-nowrap">
						<div
							class="text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]"
						>
							Team
						</div>
						<div class="truncate text-ui text-[var(--color-text-primary)]">
							{currentTeam}
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
					{#each teams as team}
						<button
							class="flex w-full items-center gap-2.5 rounded-[var(--radius-input)] px-2.5 py-2 text-ui transition-colors duration-150 hover:bg-[var(--color-bg-3)] {team ===
							currentTeam
								? 'bg-[var(--color-accent-glow)]'
								: ''}"
							onclick={() => (teamPopoverOpen = false)}
						>
							<div
								class="flex h-5 w-5 items-center justify-center rounded-[var(--radius-avatar)] text-badge font-bold uppercase text-white {team ===
								currentTeam
									? 'bg-[var(--color-accent)]'
									: 'bg-[var(--color-bg-5)]'}"
							>
								{team[0]}
							</div>
							<span
								class={team === currentTeam
									? 'font-medium text-[var(--color-text-bright)]'
									: 'text-[var(--color-text-primary)]'}
							>
								{team}
							</span>
						</button>
					{/each}
					<div class="mt-0.5 border-t border-[var(--color-border)] pt-0.5">
						<button
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
		<a
			href="/docs"
			class="group flex items-center rounded-[var(--radius-input)] px-2.5 py-2.5 text-[var(--color-text-secondary)] transition-colors duration-150 hover:bg-[var(--color-bg-3)] hover:text-[var(--color-text-primary)] {collapsed ? 'justify-center px-2' : 'gap-3'}"
			title={collapsed ? 'Docs' : undefined}
		>
			<IconDocs size={16} class="shrink-0 opacity-50 transition-opacity duration-150 group-hover:opacity-100" />
			{#if !collapsed}<span class="text-ui">Docs</span>{/if}
		</a>
		<a
			href="/dashboard/notifications"
			class="group flex items-center rounded-[var(--radius-input)] px-2.5 py-2.5 text-[var(--color-text-secondary)] transition-colors duration-150 hover:bg-[var(--color-bg-3)] hover:text-[var(--color-text-primary)] {collapsed ? 'justify-center px-2' : 'gap-3'}"
			title={collapsed ? 'Notifications' : undefined}
		>
			<IconBell size={16} class="shrink-0 opacity-50 transition-opacity duration-150 group-hover:opacity-100" />
			{#if !collapsed}<span class="text-ui">Notifications</span>{/if}
		</a>
		<a
			href="/dashboard/settings"
			class="group flex items-center rounded-[var(--radius-input)] px-2.5 py-2.5 text-[var(--color-text-secondary)] transition-colors duration-150 hover:bg-[var(--color-bg-3)] hover:text-[var(--color-text-primary)] {collapsed ? 'justify-center px-2' : 'gap-3'}"
			title={collapsed ? 'Settings' : undefined}
		>
			<IconSettings size={16} class="shrink-0 opacity-50 transition-opacity duration-150 group-hover:opacity-100" />
			{#if !collapsed}<span class="text-ui">Settings</span>{/if}
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
			{#if isActive(item.href)}
				<a
					href={item.href}
					class="group relative flex items-center rounded-[var(--radius-input)] bg-[var(--color-accent-glow-mid)] px-2.5 py-2.5 transition-colors duration-150 {collapsed
						? 'justify-center px-2'
						: 'gap-3'}"
					title={collapsed ? item.label : undefined}
				>
					{#if !collapsed}
						<div
							class="absolute left-0 top-1/2 h-5 w-[3px] -translate-y-1/2 rounded-r-full bg-[var(--color-accent)]"
						></div>
					{/if}
					<item.icon size={16} class="shrink-0 text-[var(--color-accent-bright)]" />
					{#if !collapsed}
						<span class="text-ui font-medium text-[var(--color-accent-bright)]">
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
