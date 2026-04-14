<script lang="ts">
	import { page } from '$app/stores';
	import { auth } from '$lib/auth.svelte';
	import {
		IconServer,
		IconBox,
		IconMonitor,
		IconSettings,
		IconLogout,
		IconSidebar,
		IconBell,
		IconDocs,
		IconChevron,
		IconShield,
		IconMembers,
		IconUser
	} from './icons';

	let { collapsed = $bindable(false) }: { collapsed: boolean } = $props();

	type NavItem = {
		label: string;
		icon: typeof IconServer;
		href: string;
	};

	const managementItems: NavItem[] = [
		{ label: 'Users', icon: IconUser, href: '/admin/users' },
		{ label: 'Teams', icon: IconMembers, href: '/admin/teams' }
	];

	const platformItems: NavItem[] = [
		{ label: 'Templates', icon: IconBox, href: '/admin/templates' },
		{ label: 'Capsules', icon: IconMonitor, href: '/admin/capsules' },
		{ label: 'Hosts', icon: IconServer, href: '/admin/hosts' }
	];

	function isActive(href: string): boolean {
		const p = $page.url.pathname;
		return p === href || p.startsWith(href + '/');
	}

	function toggleCollapsed() {
		collapsed = !collapsed;
		localStorage.setItem('wrenn_sidebar_collapsed', String(collapsed));
	}

	let userName = $derived(auth.name || auth.email || '');
</script>

<aside
	class="relative flex h-screen shrink-0 flex-col overflow-hidden border-r border-[var(--color-border)] bg-[var(--color-bg-1)] transition-[width] duration-250 ease-in-out"
	style="width: {collapsed ? '56px' : '230px'}"
>
	<!-- Subtle accent top-edge — marks this as an elevated context -->
	<div class="absolute inset-x-0 top-0 h-[2px] bg-gradient-to-r from-[var(--color-accent)]/60 via-[var(--color-accent)] to-[var(--color-accent)]/60"></div>

	<!-- Brand + collapse toggle -->
	<div class="flex shrink-0 items-center px-4 pt-6 pb-4 {collapsed ? 'justify-center' : 'justify-between'}">
		{#if !collapsed}
			<div class="flex items-center gap-2.5">
				<div class="relative">
					<img
						src="/logo.svg"
						alt="Wrenn"
						class="h-7 w-7 shrink-0 rounded-[var(--radius-logo)]"
					/>
				</div>
				<div class="flex flex-col gap-0.5 leading-none">
					<span class="font-brand text-[1.286rem] text-[var(--color-text-bright)]">Wrenn</span>
					<span class="inline-flex w-fit items-center gap-1 rounded-full bg-[var(--color-accent)]/15 px-1.5 py-px text-[10px] font-bold uppercase tracking-[0.1em] text-[var(--color-accent-bright)]">
						<IconShield size={8} />
						Admin
					</span>
				</div>
			</div>
		{:else}
			<!-- Collapsed: show shield as admin identity marker -->
			<div class="flex h-7 w-7 items-center justify-center rounded-[var(--radius-button)] bg-[var(--color-accent)]/10 text-[var(--color-accent-bright)]">
				<IconShield size={14} />
			</div>
		{/if}
		<button
			onclick={toggleCollapsed}
			class="flex h-7 w-7 shrink-0 items-center justify-center rounded-[var(--radius-button)] text-[var(--color-text-tertiary)] transition-colors duration-150 hover:bg-[var(--color-bg-3)] hover:text-[var(--color-text-secondary)] {collapsed ? 'absolute top-5 right-1.5' : ''}"
			title={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
		>
			<IconSidebar size={16} />
		</button>
	</div>

	<!-- Back to dashboard -->
	<div class="px-3 pb-3 {collapsed ? 'px-1.5' : ''}">
		<a
			href="/dashboard"
			class="flex items-center rounded-[var(--radius-input)] px-2.5 py-2 text-ui text-[var(--color-text-tertiary)] transition-colors duration-150 hover:bg-[var(--color-bg-3)] hover:text-[var(--color-text-secondary)] {collapsed ? 'justify-center px-2' : 'gap-2'}"
			title={collapsed ? 'Back to dashboard' : undefined}
		>
			<IconChevron size={12} direction="left" class="shrink-0" />
			{#if !collapsed}<span>Dashboard</span>{/if}
		</a>
	</div>

	<div class="mx-4 mb-3 h-px bg-[var(--color-border)] {collapsed ? 'mx-3' : ''}"></div>

	<!-- Navigation -->
	<nav class="flex-1 overflow-y-auto px-3 {collapsed ? 'px-1.5' : ''}">
		{@render navSection('Platform', platformItems)}
		{@render navSection('Management', managementItems)}
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
		class="flex shrink-0 items-center border-t border-[var(--color-border)] px-3 py-2.5 {collapsed ? 'justify-center px-1.5' : 'gap-2.5'}"
	>
		{#if !collapsed}
			<div class="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-[var(--color-bg-4)] text-badge font-bold uppercase text-[var(--color-text-secondary)]">
				{userName[0] ?? ''}
			</div>
			<span class="flex-1 truncate text-ui text-[var(--color-text-secondary)]">
				{userName}
			</span>
		{/if}
		<button
			onclick={() => auth.logout()}
			class="flex shrink-0 items-center justify-center rounded-[var(--radius-button)] transition-colors duration-150 hover:text-[var(--color-red)] {collapsed ? 'h-7 w-7 text-[var(--color-text-muted)] hover:bg-[var(--color-bg-3)]' : 'h-6 w-6 text-[var(--color-text-tertiary)]'}"
			title="Sign out"
		>
			<IconLogout size={collapsed ? 15 : 14} />
		</button>
	</div>
</aside>

{#snippet navSection(title: string, items: NavItem[])}
	<div class="mb-3">
		{#if !collapsed}
			<div class="mb-1 px-2.5 py-1.5 text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">
				{title}
			</div>
		{:else}
			<div class="mx-1 my-2 h-px bg-[var(--color-border)]"></div>
		{/if}
		{#each items as item}
			{#if isActive(item.href)}
				<a
					href={item.href}
					class="group relative flex items-center rounded-[var(--radius-input)] bg-[var(--color-accent-glow-mid)] px-2.5 py-2.5 transition-colors duration-150 {collapsed ? 'justify-center px-2' : 'gap-3'}"
					title={collapsed ? item.label : undefined}
				>
					{#if !collapsed}
						<div class="absolute left-0 top-1/2 h-5 w-[3px] -translate-y-1/2 rounded-r-full bg-[var(--color-accent)]"></div>
					{/if}
					<item.icon size={16} class="shrink-0 text-[var(--color-accent-bright)]" />
					{#if !collapsed}
						<span class="text-ui font-medium text-[var(--color-accent-bright)]">{item.label}</span>
					{/if}
				</a>
			{:else}
				<a
					href={item.href}
					class="group flex items-center rounded-[var(--radius-input)] px-2.5 py-2.5 transition-colors duration-150 hover:bg-[var(--color-bg-3)] {collapsed ? 'justify-center px-2' : 'gap-3'}"
					title={collapsed ? item.label : undefined}
				>
					<item.icon size={16} class="shrink-0 opacity-50 transition-opacity duration-150 group-hover:opacity-100" />
					{#if !collapsed}
						<span class="text-ui text-[var(--color-text-primary)] transition-colors duration-150 group-hover:text-[var(--color-text-bright)]">{item.label}</span>
					{/if}
				</a>
			{/if}
		{/each}
	</div>
{/snippet}
