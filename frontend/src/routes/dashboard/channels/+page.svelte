<script lang="ts">
	import { onMount } from 'svelte';
	import { fly } from 'svelte/transition';
	import { cubicIn, cubicOut } from 'svelte/easing';
	import {
		listChannels,
		createChannel,
		updateChannel,
		deleteChannel,
		rotateConfig,
		testChannel,
		PROVIDERS,
		EVENT_TYPES,
		type Channel
	} from '$lib/api/channels';
	import { toast } from '$lib/toast.svelte';
	import { formatDate, timeAgo } from '$lib/utils/format';

	// List state
	let channels = $state<Channel[]>([]);
	let loading = $state(true);
	let error = $state<string | null>(null);

	// Create dialog
	let showCreate = $state(false);
	let createStep = $state<1 | 2>(1);
	let createName = $state('');
	let createProvider = $state('discord');
	let createConfig = $state<Record<string, string>>({});
	let createEvents = $state<string[]>([]);
	let creating = $state(false);
	let createError = $state<string | null>(null);
	let testing = $state(false);

	// Secret reveal (webhook channels)
	let revealChannel = $state<Channel | null>(null);
	let copied = $state(false);
	let copyCount = $state(0);

	// Flash newly created row
	let flashChannelId = $state<string | null>(null);

	// Edit dialog
	let editTarget = $state<Channel | null>(null);
	let editName = $state('');
	let editEvents = $state<string[]>([]);
	let editing = $state(false);
	let editError = $state<string | null>(null);

	// Delete dialog
	let deleteTarget = $state<Channel | null>(null);
	let deleting = $state(false);
	let deleteError = $state<string | null>(null);

	// Action menu (per-row)
	let openDropdownId = $state<string | null>(null);
	let dropdownPos = $state<{ top: number; left: number }>({ top: 0, left: 0 });

	// Rotate config dialog
	let rotateTarget = $state<Channel | null>(null);
	let rotateConfig_ = $state<Record<string, string>>({});
	let rotating = $state(false);
	let rotateError = $state<string | null>(null);

	// Dropdown state (create dialog)
	let providerDropdownOpen = $state(false);
	let providerDropdownEl = $state<HTMLElement | null>(null);
	let eventsDropdownOpen = $state(false);
	let eventsDropdownEl = $state<HTMLElement | null>(null);

	// Dropdown state (edit dialog)
	let editEventsDropdownOpen = $state(false);
	let editEventsDropdownEl = $state<HTMLElement | null>(null);

	// Provider helpers
	let selectedProvider = $derived(PROVIDERS.find((p) => p.value === createProvider)!);

	let groupedEvents = $derived.by(() => {
		const groups: Record<string, typeof EVENT_TYPES[number][]> = {};
		for (const et of EVENT_TYPES) {
			(groups[et.group] ??= []).push(et);
		}
		return groups;
	});

	function providerLabel(value: string): string {
		return PROVIDERS.find((p) => p.value === value)?.label ?? value;
	}

	// Per-provider color palette — [text, bg, border, stripe]
	const PROVIDER_COLORS: Record<string, { text: string; bg: string; border: string }> = {
		discord:    { text: '#8b9cef', bg: 'rgba(88,101,242,0.12)', border: 'rgba(88,101,242,0.3)' },
		slack:      { text: '#d4a0c0', bg: 'rgba(180,120,160,0.10)', border: 'rgba(180,120,160,0.3)' },
		teams:      { text: '#a78bda', bg: 'rgba(120,90,200,0.10)', border: 'rgba(120,90,200,0.3)' },
		googlechat: { text: '#6ec07a', bg: 'rgba(60,176,80,0.10)',  border: 'rgba(60,176,80,0.25)' },
		telegram:   { text: '#6cb8d9', bg: 'rgba(42,171,226,0.10)', border: 'rgba(42,171,226,0.25)' },
		matrix:     { text: '#6bccc4', bg: 'rgba(80,200,190,0.10)', border: 'rgba(80,200,190,0.25)' },
		webhook:    { text: 'var(--color-text-secondary)', bg: 'rgba(255,255,255,0.04)', border: 'var(--color-border-mid)' },
	};

	function providerColor(provider: string): typeof PROVIDER_COLORS['discord'] {
		return PROVIDER_COLORS[provider] ?? PROVIDER_COLORS['webhook'];
	}

	function fieldLabel(field: string): string {
		return field.replace(/_/g, ' ').replace(/\b\w/g, (c) => c.toUpperCase());
	}

	function fieldPlaceholder(field: string): string {
		const placeholders: Record<string, string> = {
			webhook_url: 'https://discord.com/api/webhooks/...',
			url: 'https://your-endpoint.com/webhook',
			bot_token: 'e.g. 123456:ABC-DEF...',
			chat_id: 'e.g. -1001234567890',
			homeserver_url: 'https://matrix.org',
			access_token: 'syt_...',
			room_id: '!abcdef:matrix.org',
			secret: 'Your HMAC signing secret'
		};
		return placeholders[field] ?? '';
	}

	// Reset create form
	function resetCreateForm() {
		createStep = 1;
		createName = '';
		createProvider = 'discord';
		createConfig = {};
		createEvents = [];
		createError = null;
		testing = false;
	}

	// Step 1 is valid when name + all required config fields are filled
	let step1Valid = $derived(
		createName.trim() !== '' && selectedProvider.fields.every((f) => createConfig[f])
	);

	async function fetchChannels() {
		loading = true;
		error = null;
		const result = await listChannels();
		if (result.ok) {
			channels = result.data;
		} else {
			error = result.error;
		}
		loading = false;
	}

	async function handleCreate() {
		if (!createName.trim() || createEvents.length === 0) return;
		creating = true;
		createError = null;
		// Strip empty values so the backend can auto-generate (e.g. webhook secret)
		const config: Record<string, string> = {};
		for (const [k, v] of Object.entries(createConfig)) {
			if (v) config[k] = v;
		}
		const result = await createChannel(createName.trim(), createProvider, config, createEvents);
		if (result.ok) {
			channels = [result.data, ...channels];
			if (result.data.secret) {
				revealChannel = result.data;
			}
			showCreate = false;
			resetCreateForm();
			toast.success('Channel created');
		} else {
			createError = result.error;
		}
		creating = false;
	}

	async function handleTest() {
		testing = true;
		createError = null;
		const result = await testChannel(createProvider, createConfig);
		if (result.ok) {
			toast.success('Test notification sent');
		} else {
			createError = result.error;
		}
		testing = false;
	}

	async function handleEdit() {
		if (!editTarget || !editName.trim() || editEvents.length === 0) return;
		editing = true;
		editError = null;
		const result = await updateChannel(editTarget.id, editName.trim(), editEvents);
		if (result.ok) {
			channels = channels.map((c) => (c.id === editTarget!.id ? result.data : c));
			editTarget = null;
			toast.success('Channel updated');
		} else {
			editError = result.error;
		}
		editing = false;
	}

	async function handleDelete() {
		if (!deleteTarget) return;
		deleting = true;
		deleteError = null;
		const id = deleteTarget.id;
		const result = await deleteChannel(id);
		if (result.ok) {
			channels = channels.filter((c) => c.id !== id);
			deleteTarget = null;
			toast.success('Channel deleted');
		} else {
			deleteError = result.error;
		}
		deleting = false;
	}

	async function handleRotate() {
		if (!rotateTarget) return;
		rotating = true;
		rotateError = null;
		// Strip empty values so the backend can auto-generate (e.g. webhook secret)
		const config: Record<string, string> = {};
		for (const [k, v] of Object.entries(rotateConfig_)) {
			if (v) config[k] = v;
		}
		const result = await rotateConfig(rotateTarget.id, config);
		if (result.ok) {
			channels = channels.map((c) => (c.id === rotateTarget!.id ? result.data : c));
			rotateTarget = null;
			toast.success('Secrets rotated');
		} else {
			rotateError = result.error;
		}
		rotating = false;
	}

	function openRotate(ch: Channel) {
		const config: Record<string, string> = {};
		if (ch.provider === 'webhook') {
			// Webhook rotation only changes the secret, not the url
			config['secret'] = '';
		} else {
			const provider = PROVIDERS.find((p) => p.value === ch.provider);
			for (const f of provider?.fields ?? []) config[f] = '';
		}
		rotateConfig_ = config;
		rotateError = null;
		rotateTarget = ch;
	}

	function rotateFieldsFor(ch: Channel): string[] {
		if (ch.provider === 'webhook') return ['secret'];
		const provider = PROVIDERS.find((p) => p.value === ch.provider);
		return [...(provider?.fields ?? [])];
	}

	function dismissReveal() {
		const id = revealChannel?.id ?? null;
		revealChannel = null;
		if (id) {
			flashChannelId = id;
			setTimeout(() => { flashChannelId = null; }, 1600);
		}
	}

	async function copySecret() {
		if (!revealChannel?.secret) return;
		try {
			await navigator.clipboard.writeText(revealChannel.secret);
			copied = true;
			copyCount += 1;
			setTimeout(() => (copied = false), 2000);
		} catch {
			toast.error('Copy failed — select the secret and copy manually.');
		}
	}

	function openEdit(ch: Channel) {
		editTarget = ch;
		editName = ch.name;
		editEvents = [...ch.events];
		editError = null;
	}

	function toggleEvent(list: string[], value: string): string[] {
		return list.includes(value) ? list.filter((v) => v !== value) : [...list, value];
	}

	function eventsLabel(events: string[]): string {
		if (events.length === 0) return 'Select events';
		if (events.length === EVENT_TYPES.length) return 'All events';
		if (events.length <= 2) return events.join(', ');
		return `${events.length} events`;
	}

	// Click-outside handler — single listener covers all dropdowns
	function useClickOutside(open: () => boolean, el: () => HTMLElement | null, close: () => void) {
		$effect(() => {
			if (!open()) return;
			function onMouseDown(e: MouseEvent) {
				const container = el();
				if (container && !container.contains(e.target as Node)) close();
			}
			document.addEventListener('mousedown', onMouseDown);
			return () => document.removeEventListener('mousedown', onMouseDown);
		});
	}

	useClickOutside(() => providerDropdownOpen, () => providerDropdownEl, () => { providerDropdownOpen = false; });
	useClickOutside(() => eventsDropdownOpen, () => eventsDropdownEl, () => { eventsDropdownOpen = false; });
	useClickOutside(() => editEventsDropdownOpen, () => editEventsDropdownEl, () => { editEventsDropdownOpen = false; });

	onMount(fetchChannels);
</script>

<svelte:head>
	<title>Wrenn — Channels</title>
</svelte:head>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<svelte:window
	onkeydown={(e) => {
		if (e.key === 'Escape') {
			if (openDropdownId) { openDropdownId = null; return; }
			if (creating || editing || deleting || rotating || testing) return;
			if (showCreate) { showCreate = false; return; }
			if (revealChannel) { revealChannel = null; return; }
			editTarget = null;
			deleteTarget = null;
			rotateTarget = null;
		}
	}}
	onclick={(e) => {
		if (openDropdownId && !(e.target as Element)?.closest('.split-btn-container')) {
			openDropdownId = null;
		}
	}}
/>

<main class="flex-1 overflow-y-auto bg-[var(--color-bg-0)]">
			<!-- Header -->
			<div class="px-7 pt-8">
				<div class="flex items-center justify-between">
					<div>
						<div class="flex items-baseline gap-4">
							<h1 class="font-serif text-page text-[var(--color-text-bright)]">
								Channels
							</h1>
							{#if !loading && channels.length > 0}
								<span class="font-serif text-[1.75rem] text-[var(--color-text-muted)]">{channels.length}</span>
							{/if}
						</div>
						<p class="mt-2 text-ui text-[var(--color-text-secondary)]">
							Route capsule events to Discord, Slack, Telegram, and other destinations.
						</p>
					</div>

					<button
						onclick={() => { showCreate = true; resetCreateForm(); }}
						class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-accent)] px-4 py-2 text-ui font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0 active:scale-95"
					>
						<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
							<line x1="12" y1="5" x2="12" y2="19" /><line x1="5" y1="12" x2="19" y2="12" />
						</svg>
						New Channel
					</button>
				</div>

				<div class="mt-6 border-b border-[var(--color-border)]"></div>
			</div>

			<!-- Content -->
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
					<div class="mb-4 flex items-center justify-end">
						<div class="skeleton h-4 w-20 rounded-sm"></div>
					</div>
					<div class="overflow-hidden rounded-[var(--radius-card)] border border-[var(--color-border)]">
						<div class="grid grid-cols-[1.8fr_1fr_1.5fr_1.2fr_140px] border-b border-[var(--color-border)] bg-[var(--color-bg-3)]">
							<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Channel</div>
							<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Provider</div>
							<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Events</div>
							<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Updated</div>
							<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Actions</div>
						</div>
						{#each Array(3) as _, i}
							<div class="grid grid-cols-[1.8fr_1fr_1.5fr_1.2fr_140px] items-center border-b border-[var(--color-border)] last:border-b-0">
								<div class="px-5 py-4">
									<div class="skeleton h-3 rounded-sm" style="width: {[160, 130, 150][i]}px; animation-delay: {i * 60}ms"></div>
									<div class="skeleton mt-1.5 h-2.5 w-20 rounded-sm" style="animation-delay: {i * 60 + 30}ms"></div>
								</div>
								<div class="px-5 py-4"><div class="skeleton h-[18px] w-16 rounded-[3px]" style="animation-delay: {i * 60 + 20}ms"></div></div>
								<div class="flex gap-1 px-5 py-4">
									<div class="skeleton h-[16px] w-20 rounded-sm" style="animation-delay: {i * 60 + 40}ms"></div>
									<div class="skeleton h-[16px] w-16 rounded-sm" style="animation-delay: {i * 60 + 60}ms"></div>
								</div>
								<div class="px-5 py-4"><div class="skeleton h-3 w-12 rounded-sm" style="animation-delay: {i * 60 + 80}ms"></div></div>
								<div class="flex items-center justify-end px-3 py-3"><div class="skeleton h-7 w-[72px] rounded-[var(--radius-button)]" style="animation-delay: {i * 60 + 100}ms"></div></div>
							</div>
						{/each}
					</div>
				{:else if channels.length === 0}
					<!-- Empty state -->
					<div class="flex flex-col items-center justify-center py-[72px]">
						<div class="relative mb-5">
							<div class="absolute inset-0 -m-4 rounded-full" style="background: radial-gradient(circle, rgba(94,140,88,0.08) 0%, transparent 70%)"></div>
							<div
								class="relative flex h-14 w-14 items-center justify-center rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-3)]"
								style="animation: iconFloat 4s ease-in-out infinite"
							>
								<svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="var(--color-text-secondary)" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
									<circle cx="12" cy="12" r="2" />
									<path d="M16.24 7.76a6 6 0 0 1 0 8.49" />
									<path d="M7.76 16.24a6 6 0 0 1 0-8.49" />
									<path d="M19.07 4.93a10 10 0 0 1 0 14.14" />
									<path d="M4.93 19.07a10 10 0 0 1 0-14.14" />
								</svg>
							</div>
						</div>
						<p class="font-serif text-heading text-[var(--color-text-bright)]">No channels yet</p>
						<p class="mt-1.5 max-w-[340px] text-center text-ui text-[var(--color-text-tertiary)]">Channels deliver capsule events to your team's tools. Connect Discord, Slack, or a custom webhook.</p>
						<button
							onclick={() => { showCreate = true; resetCreateForm(); }}
							class="mt-6 flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-accent)] px-5 py-2.5 text-ui font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0 active:scale-95"
						>
							New Channel
							<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
								<line x1="12" y1="5" x2="12" y2="19" /><line x1="5" y1="12" x2="19" y2="12" />
							</svg>
						</button>
					</div>
				{:else}
					<!-- Table -->
					<div class="overflow-hidden rounded-[var(--radius-card)] border border-[var(--color-border)]">
						<!-- Header -->
						<div class="grid grid-cols-[1.8fr_1fr_1.5fr_1.2fr_140px] border-b border-[var(--color-border)] bg-[var(--color-bg-3)]">
							<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Channel</div>
							<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Provider</div>
							<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Events</div>
							<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Updated</div>
							<div class="px-5 py-3 text-right text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Actions</div>
						</div>

						<!-- Rows -->
						{#each channels as ch, i (ch.id)}
							<div
								class="channel-row relative grid grid-cols-[1.8fr_1fr_1.5fr_1.2fr_140px] items-center overflow-hidden border-b border-[var(--color-border)] transition-colors duration-150 last:border-b-0 {flashChannelId === ch.id ? 'channel-born' : ''}"
								in:fly={{ y: 6, duration: 350, delay: i * 40, easing: cubicOut }}
								out:fly={{ x: -12, duration: 180, easing: cubicIn }}
							>
								<div class="row-stripe pointer-events-none absolute left-0 top-0 h-full w-[3px]" style="background: {providerColor(ch.provider).text}"></div>

								<!-- Name -->
								<div class="min-w-0 px-5 py-4">
									<span class="truncate text-ui font-medium text-[var(--color-text-bright)]">{ch.name}</span>
									<div class="mt-0.5 font-mono text-badge text-[var(--color-text-muted)]">{ch.id}</div>
								</div>

								<!-- Provider -->
								<div class="px-5 py-4">
									<span
										class="inline-flex items-center gap-1.5 rounded-[3px] border px-2.5 py-1 text-badge font-semibold uppercase tracking-[0.04em]"
										style="color: {providerColor(ch.provider).text}; background: {providerColor(ch.provider).bg}; border-color: {providerColor(ch.provider).border}"
									>
										{@render providerIcon(ch.provider)}
										{providerLabel(ch.provider)}
									</span>
								</div>

								<!-- Events -->
								<div class="flex flex-wrap gap-1 px-5 py-4">
									{#if ch.events.length <= 3}
										{#each ch.events as ev}
											<span class="rounded-sm bg-[var(--color-bg-4)] px-1.5 py-0.5 font-mono text-badge text-[var(--color-text-tertiary)]">{ev}</span>
										{/each}
									{:else}
										{#each ch.events.slice(0, 2) as ev}
											<span class="rounded-sm bg-[var(--color-bg-4)] px-1.5 py-0.5 font-mono text-badge text-[var(--color-text-tertiary)]">{ev}</span>
										{/each}
										<span class="rounded-sm bg-[var(--color-bg-4)] px-1.5 py-0.5 text-badge text-[var(--color-text-muted)]">+{ch.events.length - 2}</span>
									{/if}
								</div>

								<!-- Updated -->
								<div class="px-5 py-4">
									<span class="text-ui text-[var(--color-text-secondary)]" title={formatDate(ch.updated_at)}>
										{timeAgo(ch.updated_at)}
									</span>
								</div>

								<!-- Actions: split button -->
								<div class="flex items-center justify-end px-3 py-3">
									<div class="split-btn-container relative flex items-stretch overflow-hidden rounded-[var(--radius-button)] border border-[var(--color-border-mid)] bg-[var(--color-bg-3)] transition-shadow duration-200 hover:shadow-[0_0_0_1px_var(--color-border-mid),0_0_8px_rgba(94,140,88,0.06)]">
										<!-- Edit part -->
										<button
											onclick={() => openEdit(ch)}
											class="flex items-center px-3 py-1.5 text-meta font-medium text-[var(--color-text-primary)] transition-all duration-150 hover:bg-[var(--color-bg-4)] hover:text-[var(--color-text-bright)] active:scale-95"
										>
											Edit
										</button>
										<!-- Divider -->
										<div class="w-px shrink-0 bg-[var(--color-border-mid)]"></div>
										<!-- Chevron / dropdown trigger -->
										<button
											onclick={(e) => {
												e.stopPropagation();
												if (openDropdownId === ch.id) {
													openDropdownId = null;
												} else {
													const rect = (e.currentTarget as HTMLElement).getBoundingClientRect();
													dropdownPos = { top: rect.bottom + 4, left: rect.right - 160 };
													openDropdownId = ch.id;
												}
											}}
											aria-label="More actions"
											class="flex items-center px-2 py-1.5 text-[var(--color-text-secondary)] transition-colors duration-150 hover:bg-[var(--color-bg-4)] hover:text-[var(--color-text-bright)]"
										>
											<svg
												class="transition-transform duration-150 {openDropdownId === ch.id ? 'rotate-180' : ''}"
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
				{/if}
			</div>
		</main>

		<!-- Status bar -->
		<footer class="flex h-7 shrink-0 items-center justify-end border-t border-[var(--color-border)] bg-[var(--color-bg-1)] px-7">
			<div class="flex items-center gap-1.5">
				<span class="relative flex h-[5px] w-[5px]">
					<span class="animate-status-ping absolute inline-flex h-full w-full rounded-full bg-[var(--color-accent)]"></span>
					<span class="relative inline-flex h-[5px] w-[5px] rounded-full bg-[var(--color-accent)]"></span>
				</span>
				<span class="font-mono text-label uppercase tracking-[0.04em] text-[var(--color-text-secondary)]">All systems operational</span>
			</div>
		</footer>

<!-- Split button dropdown -->
{#if openDropdownId}
	{@const dropdownChannel = channels.find((c) => c.id === openDropdownId)}
	{#if dropdownChannel}
		<div
			class="fixed z-50 w-40 overflow-hidden rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] py-1"
			style="top: {dropdownPos.top}px; left: {dropdownPos.left}px; animation: fadeUp 0.15s ease both"
		>
			<button
				onclick={(e) => {
					e.stopPropagation();
					const ch = channels.find((c) => c.id === openDropdownId);
					openDropdownId = null;
					if (ch) openRotate(ch);
				}}
				class="flex w-full items-center gap-2 px-3 py-2 text-meta text-[var(--color-text-secondary)] transition-colors duration-150 hover:bg-[var(--color-bg-3)] hover:text-[var(--color-text-primary)]"
			>
				<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="shrink-0">
					<polyline points="23 4 23 10 17 10" />
					<path d="M20.49 15a9 9 0 1 1-2.12-9.36L23 10" />
				</svg>
				Rotate Secret
			</button>
			<div class="my-1 border-t border-[var(--color-border)]"></div>
			<button
				onclick={(e) => {
					e.stopPropagation();
					const ch = channels.find((c) => c.id === openDropdownId);
					openDropdownId = null;
					if (ch) { deleteError = null; deleteTarget = ch; }
				}}
				class="flex w-full items-center gap-2 px-3 py-2 text-meta text-[var(--color-red)] transition-colors duration-150 hover:bg-[var(--color-red)]/5"
			>
				<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="shrink-0">
					<polyline points="3 6 5 6 21 6" />
					<path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2" />
				</svg>
				Delete
			</button>
		</div>
	{/if}
{/if}

<!-- Create Channel Dialog -->
{#if showCreate}
	<div class="fixed inset-0 z-50 flex items-center justify-center">
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="absolute inset-0 bg-black/60"
			onclick={() => { if (!creating && !testing) showCreate = false; }}
			onkeydown={(e) => { if (e.key === 'Escape' && !creating && !testing) showCreate = false; }}
		></div>

		<div class="relative w-full max-w-[520px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] p-6" style="animation: fadeUp 0.2s ease both; box-shadow: var(--shadow-dialog)">

			<!-- Step indicator -->
			<div class="mb-5 flex items-center gap-3">
				<div class="flex items-center gap-2">
					<span class="flex h-5 w-5 items-center justify-center rounded-full text-[10px] font-bold leading-none
						{createStep === 1 ? 'bg-[var(--color-accent)] text-white' : 'bg-[var(--color-accent-glow-mid)] text-[var(--color-accent-bright)]'}">
						{#if createStep === 2}
							<svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round">
								<polyline points="20 6 9 17 4 12" />
							</svg>
						{:else}
							1
						{/if}
					</span>
					<span class="text-meta font-medium {createStep === 1 ? 'text-[var(--color-text-bright)]' : 'text-[var(--color-text-tertiary)]'}">Connection</span>
				</div>
				<div class="h-px flex-1 bg-[var(--color-border)] {createStep === 2 ? 'bg-[var(--color-accent)]/30' : ''}"></div>
				<div class="flex items-center gap-2">
					<span class="flex h-5 w-5 items-center justify-center rounded-full text-[10px] font-bold leading-none
						{createStep === 2 ? 'bg-[var(--color-accent)] text-white' : 'bg-[var(--color-bg-4)] text-[var(--color-text-muted)]'}">
						2
					</span>
					<span class="text-meta font-medium {createStep === 2 ? 'text-[var(--color-text-bright)]' : 'text-[var(--color-text-muted)]'}">Events</span>
				</div>
			</div>

			{#if createError}
				<div class="mb-4 rounded-[var(--radius-input)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-3 py-2 text-meta text-[var(--color-red)]">
					{createError}
				</div>
			{/if}

			{#if createStep === 1}
				<!-- Step 1: Connection -->
				<h2 class="font-serif text-heading text-[var(--color-text-bright)]">New Channel</h2>
				<p class="mt-1 text-ui text-[var(--color-text-tertiary)]">Name the channel, pick a provider, and enter its connection details.</p>

				<!-- Name -->
				<div class="mt-5">
					<label class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]" for="channel-name">
						Channel name
					</label>
					<input
						id="channel-name"
						type="text"
						placeholder="e.g. Ops Alerts"
						bind:value={createName}
						disabled={creating}
						class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-4)] px-3 py-2 text-ui text-[var(--color-text-bright)] outline-none placeholder:text-[var(--color-text-muted)] transition-colors duration-150 focus:border-[var(--color-accent)] disabled:opacity-60"
					/>
				</div>

				<!-- Provider -->
				<div class="mt-4">
					<label for="provider-select" class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]">
						Provider
					</label>
					<div class="relative" bind:this={providerDropdownEl}>
						<button
							id="provider-select"
							onclick={() => { providerDropdownOpen = !providerDropdownOpen; }}
							disabled={creating}
							class="flex w-full items-center justify-between rounded-[var(--radius-input)] border px-3 py-2 text-ui transition-colors duration-150 disabled:opacity-60
								{providerDropdownOpen
									? 'border-[var(--color-accent)] bg-[var(--color-bg-4)]'
									: 'border-[var(--color-border)] bg-[var(--color-bg-4)] hover:border-[var(--color-border-mid)]'}"
						>
							<span class="flex items-center gap-2 text-[var(--color-text-bright)]">
								{@render providerIcon(createProvider)}
								{providerLabel(createProvider)}
							</span>
							<svg
								class="transition-transform duration-150 text-[var(--color-text-tertiary)] {providerDropdownOpen ? 'rotate-180' : ''}"
								width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"
							>
								<polyline points="6 9 12 15 18 9" />
							</svg>
						</button>

						{#if providerDropdownOpen}
							<div
								class="absolute left-0 top-full z-20 mt-1.5 w-full overflow-y-auto rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] py-1.5 shadow-xl"
								style="animation: fadeUp 0.12s ease both"
							>
								{#each PROVIDERS as p}
									{@const ppc = providerColor(p.value)}
									<button
										class="flex w-full items-center gap-2.5 px-3 py-2 text-ui transition-colors duration-100 hover:bg-[var(--color-bg-3)]
											{createProvider === p.value ? 'font-medium' : 'text-[var(--color-text-primary)]'}"
										style={createProvider === p.value ? `color: ${ppc.text}; background: ${ppc.bg}` : ''}
										onclick={() => { createProvider = p.value; createConfig = {}; providerDropdownOpen = false; }}
									>
										{@render providerIcon(p.value)}
										{p.label}
										{#if createProvider === p.value}
											<svg class="ml-auto" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
												<polyline points="20 6 9 17 4 12" />
											</svg>
										{/if}
									</button>
								{/each}
							</div>
						{/if}
					</div>
				</div>

				<!-- Config fields -->
				<div class="mt-4 space-y-3">
					{#each selectedProvider.fields as field}
						<div>
							<label class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]" for="config-{field}">
								{fieldLabel(field)}
							</label>
							<input
								id="config-{field}"
								type={field.includes('token') || field.includes('secret') ? 'password' : 'text'}
								placeholder={fieldPlaceholder(field)}
								value={createConfig[field] ?? ''}
								oninput={(e) => { createConfig = { ...createConfig, [field]: e.currentTarget.value }; }}
								disabled={creating}
								class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-4)] px-3 py-2 font-mono text-meta text-[var(--color-text-bright)] outline-none placeholder:text-[var(--color-text-muted)] placeholder:font-sans transition-colors duration-150 focus:border-[var(--color-accent)] disabled:opacity-60"
							/>
						</div>
					{/each}

					{#if createProvider === 'webhook'}
						<div>
							<label class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]" for="config-secret">
								Secret <span class="font-normal normal-case text-[var(--color-text-muted)]">(optional — auto-generated if blank)</span>
							</label>
							<input
								id="config-secret"
								type="password"
								placeholder="Leave blank to auto-generate"
								value={createConfig['secret'] ?? ''}
								oninput={(e) => { createConfig = { ...createConfig, secret: e.currentTarget.value }; }}
								disabled={creating}
								class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-4)] px-3 py-2 font-mono text-meta text-[var(--color-text-bright)] outline-none placeholder:text-[var(--color-text-muted)] placeholder:font-sans transition-colors duration-150 focus:border-[var(--color-accent)] disabled:opacity-60"
							/>
						</div>
					{/if}
				</div>

				<!-- Step 1 Actions -->
				<div class="mt-6 flex items-center justify-between">
					<button
						onclick={handleTest}
						disabled={testing || !step1Valid}
						class="flex items-center gap-1.5 rounded-[var(--radius-button)] border border-[var(--color-border)] px-3 py-2 text-meta text-[var(--color-text-secondary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)] disabled:opacity-40 disabled:hover:border-[var(--color-border)]"
					>
						{#if testing}
							<svg class="animate-spin" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
								<path d="M21 12a9 9 0 1 1-6.219-8.56" />
							</svg>
							Testing...
						{:else}
							<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
								<polygon points="5 3 19 12 5 21 5 3" />
							</svg>
							Test
						{/if}
					</button>

					<div class="flex gap-3">
						<button
							onclick={() => { showCreate = false; }}
							class="rounded-[var(--radius-button)] border border-[var(--color-border)] px-4 py-2 text-ui text-[var(--color-text-secondary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)]"
						>
							Cancel
						</button>
						<button
							onclick={() => { createError = null; createStep = 2; }}
							disabled={!step1Valid}
							class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-accent)] px-5 py-2 text-ui font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0 active:scale-95 disabled:opacity-50 disabled:hover:translate-y-0"
						>
							Continue
							<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
								<polyline points="9 18 15 12 9 6" />
							</svg>
						</button>
					</div>
				</div>

			{:else}
				<!-- Step 2: Events -->
				<h2 class="font-serif text-heading text-[var(--color-text-bright)]">Choose Events</h2>
				<p class="mt-1 text-ui text-[var(--color-text-tertiary)]">
					Pick the events that trigger a notification to
					<span class="font-medium text-[var(--color-text-secondary)]">{createName}</span>
					via {providerLabel(createProvider)}.
				</p>

				<!-- Events dropdown -->
				<div class="mt-5">
					<label for="events-select" class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]">
						Events
					</label>
					<div class="relative" bind:this={eventsDropdownEl}>
						<button
							id="events-select"
							onclick={() => { eventsDropdownOpen = !eventsDropdownOpen; }}
							disabled={creating}
							class="flex w-full items-center justify-between rounded-[var(--radius-input)] border px-3 py-2 text-ui transition-colors duration-150 disabled:opacity-60
								{eventsDropdownOpen
									? 'border-[var(--color-accent)] bg-[var(--color-bg-4)]'
									: createEvents.length > 0
										? 'border-[var(--color-accent)]/60 bg-[var(--color-accent)]/10'
										: 'border-[var(--color-border)] bg-[var(--color-bg-4)] hover:border-[var(--color-border-mid)]'}"
						>
							<span class="{createEvents.length > 0 ? 'font-medium text-[var(--color-accent-bright)]' : 'text-[var(--color-text-muted)]'}">
								{eventsLabel(createEvents)}
							</span>
							<div class="flex items-center gap-2">
								{#if createEvents.length > 0}
									<span class="flex h-4 w-4 items-center justify-center rounded-full bg-[var(--color-accent)] text-[10px] font-semibold leading-none text-white">
										{createEvents.length}
									</span>
								{/if}
								<svg
									class="transition-transform duration-150 text-[var(--color-text-tertiary)] {eventsDropdownOpen ? 'rotate-180' : ''}"
									width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"
								>
									<polyline points="6 9 12 15 18 9" />
								</svg>
							</div>
						</button>

						{#if eventsDropdownOpen}
							<div
								class="absolute left-0 top-full z-20 mt-1.5 w-full overflow-y-auto rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] py-1.5 shadow-xl"
								style="max-height: 280px; animation: fadeUp 0.12s ease both"
							>
								{@render eventsDropdownItems(createEvents, (v) => { createEvents = toggleEvent(createEvents, v); })}
							</div>
						{/if}
					</div>
				</div>

				<!-- Selected events tags -->
				{#if createEvents.length > 0}
					<div class="mt-3 flex flex-wrap gap-1.5" style="animation: fadeUp 0.15s ease both">
						{#each createEvents as ev}
							<span class="flex items-center gap-1 rounded-full border border-[var(--color-accent)]/40 bg-[var(--color-accent)]/10 px-2 py-0.5 font-mono text-badge text-[var(--color-accent)]">
								{ev}
								<button
									onclick={() => { createEvents = createEvents.filter((e) => e !== ev); }}
									aria-label="Remove {ev}"
									class="flex items-center justify-center text-[var(--color-accent)] opacity-60 transition-opacity duration-100 hover:opacity-100"
								>
									<svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round">
										<line x1="18" y1="6" x2="6" y2="18" /><line x1="6" y1="6" x2="18" y2="18" />
									</svg>
								</button>
							</span>
						{/each}
					</div>
				{/if}

				<!-- Step 2 Actions -->
				<div class="mt-6 flex justify-between">
					<button
						onclick={() => { createError = null; createStep = 1; }}
						disabled={creating}
						class="flex items-center gap-1.5 rounded-[var(--radius-button)] border border-[var(--color-border)] px-4 py-2 text-ui text-[var(--color-text-secondary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)] disabled:opacity-50"
					>
						<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
							<polyline points="15 18 9 12 15 6" />
						</svg>
						Back
					</button>

					<button
						onclick={handleCreate}
						disabled={creating || createEvents.length === 0}
						class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-accent)] px-5 py-2 text-ui font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0 active:scale-95 disabled:opacity-50 disabled:hover:translate-y-0"
					>
						{#if creating}
							<svg class="animate-spin" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
								<path d="M21 12a9 9 0 1 1-6.219-8.56" />
							</svg>
							Creating...
						{:else}
							Create Channel
						{/if}
					</button>
				</div>
			{/if}
		</div>
	</div>
{/if}

<!-- Webhook Secret Reveal Dialog -->
{#if revealChannel}
	<div class="fixed inset-0 z-50 flex items-center justify-center">
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="absolute inset-0 bg-black/60"
			onclick={dismissReveal}
			onkeydown={(e) => { if (e.key === 'Escape') dismissReveal(); }}
		></div>

		<div class="relative w-full max-w-[480px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] p-6" style="animation: fadeUp 0.2s ease both; box-shadow: var(--shadow-dialog)">
			<!-- Success indicator -->
			<div class="mb-4 flex items-center gap-2.5">
				<span class="flex h-5 w-5 shrink-0 items-center justify-center rounded-full bg-[var(--color-accent-glow-mid)]" style="animation: circlePop 0.4s cubic-bezier(0.34, 1.56, 0.64, 1) both">
					<svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="var(--color-accent-bright)" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
						<polyline points="20 6 9 17 4 12" style="stroke-dasharray: 24; animation: checkDraw 0.35s 0.2s ease both" />
					</svg>
				</span>
				<span class="text-meta font-semibold text-[var(--color-accent-mid)]" style="animation: fadeUp 0.3s 0.15s ease both">Channel created</span>
			</div>

			<h2 class="font-serif text-heading text-[var(--color-text-bright)]">{revealChannel.name}</h2>
			<p class="mt-1 text-ui text-[var(--color-text-tertiary)]">
				Copy the webhook signing secret now — it won't be shown again.
			</p>

			<!-- Secret display -->
			<div class="mt-5 rounded-[var(--radius-input)] border bg-[var(--color-bg-0)] p-4" style="animation: keyRevealGlow 1.4s 0.1s ease-out both">
				<div class="flex items-center gap-3">
					<span class="min-w-0 flex-1 break-all font-mono text-ui leading-relaxed text-[var(--color-text-bright)]">
						{revealChannel.secret ?? ''}
					</span>
					{#key copyCount}
						<button
							onclick={copySecret}
							style={copied ? 'animation: copyBounce 0.35s cubic-bezier(0.34, 1.56, 0.64, 1) both' : ''}
							class="shrink-0 flex items-center gap-1.5 rounded-[var(--radius-button)] border px-3 py-1.5 text-meta font-semibold transition-all duration-150
								{copied
									? 'border-[var(--color-accent)]/40 bg-[var(--color-accent-glow-mid)] text-[var(--color-accent-mid)]'
									: 'border-[var(--color-border-mid)] text-[var(--color-text-secondary)] hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)]'}"
						>
							{#if copied}
								<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
									<polyline points="20 6 9 17 4 12" />
								</svg>
								Copied
							{:else}
								<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
									<rect x="9" y="9" width="13" height="13" rx="2" ry="2" />
									<path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1" />
								</svg>
								Copy
							{/if}
						</button>
					{/key}
				</div>
			</div>

			<!-- Warning -->
			<div class="mt-3 flex items-start gap-2 rounded-[var(--radius-input)] border border-[var(--color-amber)]/20 bg-[var(--color-amber)]/5 px-3 py-2.5">
				<svg class="mt-0.5 shrink-0" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="var(--color-amber)" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
					<path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z" />
					<line x1="12" y1="9" x2="12" y2="13" /><line x1="12" y1="17" x2="12.01" y2="17" />
				</svg>
				<p class="text-meta leading-relaxed text-[var(--color-amber)]">
					Use this secret to verify webhook signatures (HMAC-SHA256). It cannot be retrieved after you close this dialog.
				</p>
			</div>

			<div class="mt-6 flex justify-end">
				<button
					onclick={dismissReveal}
					class="rounded-[var(--radius-button)] bg-[var(--color-bg-4)] border border-[var(--color-border-mid)] px-5 py-2 text-ui font-semibold text-[var(--color-text-primary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:bg-[var(--color-bg-5)]"
				>
					Done
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Edit Channel Dialog -->
{#if editTarget}
	<div class="fixed inset-0 z-50 flex items-center justify-center">
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="absolute inset-0 bg-black/60"
			onclick={() => { if (!editing) editTarget = null; }}
			onkeydown={(e) => { if (e.key === 'Escape' && !editing) editTarget = null; }}
		></div>

		<div class="relative w-full max-w-[480px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] p-6" style="animation: fadeUp 0.2s ease both; box-shadow: var(--shadow-dialog)">
			<h2 class="font-serif text-heading text-[var(--color-text-bright)]">Edit Channel</h2>
			<p class="mt-1 text-ui text-[var(--color-text-tertiary)]">
				Update the name or subscribed events. To change the provider, delete this channel and create a new one.
			</p>

			<div class="mt-2">
				<span class="inline-flex items-center gap-1.5 rounded-sm border px-2 py-0.5 text-meta font-medium" style="color: {providerColor(editTarget.provider).text}; background: {providerColor(editTarget.provider).bg}; border-color: {providerColor(editTarget.provider).border}">
					{@render providerIcon(editTarget.provider)}
					{providerLabel(editTarget.provider)}
				</span>
			</div>

			{#if editError}
				<div class="mt-4 rounded-[var(--radius-input)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-3 py-2 text-meta text-[var(--color-red)]">
					{editError}
				</div>
			{/if}

			<!-- Name -->
			<div class="mt-5">
				<label class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]" for="edit-name">
					Channel name
				</label>
				<input
					id="edit-name"
					type="text"
					bind:value={editName}
					disabled={editing}
					class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-4)] px-3 py-2 text-ui text-[var(--color-text-bright)] outline-none placeholder:text-[var(--color-text-muted)] transition-colors duration-150 focus:border-[var(--color-accent)] disabled:opacity-60"
				/>
			</div>

			<!-- Events -->
			<div class="mt-5">
				<label for="edit-events-select" class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]">
					Events
				</label>
				<div class="relative" bind:this={editEventsDropdownEl}>
					<button
						id="edit-events-select"
						onclick={() => { editEventsDropdownOpen = !editEventsDropdownOpen; }}
						disabled={editing}
						class="flex w-full items-center justify-between rounded-[var(--radius-input)] border px-3 py-2 text-ui transition-colors duration-150 disabled:opacity-60
							{editEventsDropdownOpen
								? 'border-[var(--color-accent)] bg-[var(--color-bg-4)]'
								: editEvents.length > 0
									? 'border-[var(--color-accent)]/60 bg-[var(--color-accent)]/10'
									: 'border-[var(--color-border)] bg-[var(--color-bg-4)] hover:border-[var(--color-border-mid)]'}"
					>
						<span class="{editEvents.length > 0 ? 'font-medium text-[var(--color-accent-bright)]' : 'text-[var(--color-text-muted)]'}">
							{eventsLabel(editEvents)}
						</span>
						<div class="flex items-center gap-2">
							{#if editEvents.length > 0}
								<span class="flex h-4 w-4 items-center justify-center rounded-full bg-[var(--color-accent)] text-[10px] font-semibold leading-none text-white">
									{editEvents.length}
								</span>
							{/if}
							<svg
								class="transition-transform duration-150 text-[var(--color-text-tertiary)] {editEventsDropdownOpen ? 'rotate-180' : ''}"
								width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"
							>
								<polyline points="6 9 12 15 18 9" />
							</svg>
						</div>
					</button>

					{#if editEventsDropdownOpen}
						<div
							class="absolute left-0 top-full z-20 mt-1.5 w-full overflow-y-auto rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] py-1.5 shadow-xl"
							style="max-height: 280px; animation: fadeUp 0.12s ease both"
						>
							{@render eventsDropdownItems(editEvents, (v) => { editEvents = toggleEvent(editEvents, v); })}
						</div>
					{/if}
				</div>
			</div>

			<div class="mt-6 flex justify-end gap-3">
				<button
					onclick={() => { editTarget = null; }}
					disabled={editing}
					class="rounded-[var(--radius-button)] border border-[var(--color-border)] px-4 py-2 text-ui text-[var(--color-text-secondary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)] disabled:opacity-50"
				>
					Cancel
				</button>
				<button
					onclick={handleEdit}
					disabled={editing || !editName.trim() || editEvents.length === 0}
					class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-accent)] px-5 py-2 text-ui font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0 active:scale-95 disabled:opacity-50 disabled:hover:translate-y-0"
				>
					{#if editing}
						<svg class="animate-spin" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
							<path d="M21 12a9 9 0 1 1-6.219-8.56" />
						</svg>
						Saving...
					{:else}
						Save Changes
					{/if}
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Delete Confirmation Dialog -->
{#if deleteTarget}
	<div class="fixed inset-0 z-50 flex items-center justify-center">
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="absolute inset-0 bg-black/60"
			onclick={() => { if (!deleting) deleteTarget = null; }}
			onkeydown={(e) => { if (e.key === 'Escape' && !deleting) deleteTarget = null; }}
		></div>

		<div class="relative w-full max-w-[380px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] p-6" style="animation: fadeUp 0.2s ease both; box-shadow: var(--shadow-dialog)">
			<h2 class="font-serif text-heading text-[var(--color-text-bright)]">Delete Channel</h2>
			<p class="mt-2 text-ui text-[var(--color-text-tertiary)]">
				Permanently delete <span class="font-medium text-[var(--color-text-secondary)]">{deleteTarget.name}</span>?
				Events will stop being delivered to this destination immediately.
			</p>
			<span class="mt-2 inline-flex items-center gap-1.5 rounded-sm border px-2 py-0.5 text-meta font-medium" style="color: {providerColor(deleteTarget.provider).text}; background: {providerColor(deleteTarget.provider).bg}; border-color: {providerColor(deleteTarget.provider).border}">
				{@render providerIcon(deleteTarget.provider)}
				{providerLabel(deleteTarget.provider)}
			</span>

			{#if deleteError}
				<div class="mt-4 rounded-[var(--radius-input)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-3 py-2 text-meta text-[var(--color-red)]">
					{deleteError}
				</div>
			{/if}

			<div class="mt-6 flex justify-end gap-3">
				<button
					onclick={() => { deleteTarget = null; }}
					disabled={deleting}
					class="rounded-[var(--radius-button)] border border-[var(--color-border)] px-4 py-2 text-ui text-[var(--color-text-secondary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)] disabled:opacity-50"
				>
					Cancel
				</button>
				<button
					onclick={handleDelete}
					disabled={deleting}
					class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-red)] px-5 py-2 text-ui font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0 active:scale-95 disabled:opacity-50 disabled:hover:translate-y-0"
				>
					{#if deleting}
						<svg class="animate-spin" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
							<path d="M21 12a9 9 0 1 1-6.219-8.56" />
						</svg>
						Deleting...
					{:else}
						Delete Channel
					{/if}
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Rotate Secret Dialog -->
{#if rotateTarget}
	<div class="fixed inset-0 z-50 flex items-center justify-center">
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="absolute inset-0 bg-black/60"
			onclick={() => { if (!rotating) rotateTarget = null; }}
			onkeydown={(e) => { if (e.key === 'Escape' && !rotating) rotateTarget = null; }}
		></div>

		<div class="relative w-full max-w-[460px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] p-6" style="animation: fadeUp 0.2s ease both; box-shadow: var(--shadow-dialog)">
			<h2 class="font-serif text-heading text-[var(--color-text-bright)]">
				{rotateTarget.provider === 'webhook' ? 'Rotate Signing Secret' : 'Rotate Credentials'}
			</h2>
			<p class="mt-1 text-ui text-[var(--color-text-tertiary)]">
				{#if rotateTarget.provider === 'webhook'}
					Replace the HMAC signing secret for <span class="font-medium text-[var(--color-text-secondary)]">{rotateTarget.name}</span>. The webhook URL stays the same.
				{:else}
					Replace the connection credentials for <span class="font-medium text-[var(--color-text-secondary)]">{rotateTarget.name}</span>. This takes effect immediately.
				{/if}
			</p>

			<div class="mt-2">
				<span class="inline-flex items-center gap-1.5 rounded-sm border px-2 py-0.5 text-meta font-medium" style="color: {providerColor(rotateTarget.provider).text}; background: {providerColor(rotateTarget.provider).bg}; border-color: {providerColor(rotateTarget.provider).border}">
					{@render providerIcon(rotateTarget.provider)}
					{providerLabel(rotateTarget.provider)}
				</span>
			</div>

			{#if rotateError}
				<div class="mt-4 rounded-[var(--radius-input)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-3 py-2 text-meta text-[var(--color-red)]">
					{rotateError}
				</div>
			{/if}

			<div class="mt-5 space-y-3">
				{#each rotateFieldsFor(rotateTarget) as field}
					<div>
						<label class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]" for="rotate-{field}">
							{fieldLabel(field)}
						</label>
						<input
							id="rotate-{field}"
							type={field.includes('token') || field.includes('secret') ? 'password' : 'text'}
							placeholder={fieldPlaceholder(field)}
							value={rotateConfig_[field] ?? ''}
							oninput={(e) => { rotateConfig_ = { ...rotateConfig_, [field]: e.currentTarget.value }; }}
							disabled={rotating}
							class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-4)] px-3 py-2 font-mono text-meta text-[var(--color-text-bright)] outline-none placeholder:text-[var(--color-text-muted)] placeholder:font-sans transition-colors duration-150 focus:border-[var(--color-accent)] disabled:opacity-60"
						/>
					</div>
				{/each}
			</div>

			<!-- Warning -->
			<div class="mt-4 flex items-start gap-2 rounded-[var(--radius-input)] border border-[var(--color-amber)]/20 bg-[var(--color-amber)]/5 px-3 py-2.5">
				<svg class="mt-0.5 shrink-0" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="var(--color-amber)" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
					<path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z" />
					<line x1="12" y1="9" x2="12" y2="13" /><line x1="12" y1="17" x2="12.01" y2="17" />
				</svg>
				<p class="text-meta leading-relaxed text-[var(--color-amber)]">
					{#if rotateTarget.provider === 'webhook'}
						Your endpoint must verify signatures with the new secret. Old signatures will fail immediately.
					{:else}
						Old credentials stop working immediately. Make sure the new values are configured in your destination first.
					{/if}
				</p>
			</div>

			<div class="mt-6 flex justify-end gap-3">
				<button
					onclick={() => { rotateTarget = null; }}
					disabled={rotating}
					class="rounded-[var(--radius-button)] border border-[var(--color-border)] px-4 py-2 text-ui text-[var(--color-text-secondary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)] disabled:opacity-50"
				>
					Cancel
				</button>
				<button
					onclick={handleRotate}
					disabled={rotating || !rotateFieldsFor(rotateTarget).every((f) => rotateConfig_[f])}
					class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-amber)] px-5 py-2 text-ui font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0 active:scale-95 disabled:opacity-50 disabled:hover:translate-y-0"
				>
					{#if rotating}
						<svg class="animate-spin" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
							<path d="M21 12a9 9 0 1 1-6.219-8.56" />
						</svg>
						Rotating...
					{:else}
						{rotateTarget.provider === 'webhook' ? 'Rotate Secret' : 'Rotate Credentials'}
					{/if}
				</button>
			</div>
		</div>
	</div>
{/if}

{#snippet eventsDropdownItems(selected: string[], toggle: (value: string) => void)}
	{#each Object.entries(groupedEvents) as [group, events], gi}
		<div class="px-3 py-1.5 text-badge font-semibold uppercase tracking-[0.06em] text-[var(--color-text-muted)]">{group}</div>

		{#each events as et}
			{@const checked = selected.includes(et.value)}
			<label class="flex cursor-pointer items-center gap-2.5 px-3 py-2 transition-colors duration-100 hover:bg-[var(--color-bg-3)]">
				<span class="flex h-3.5 w-3.5 shrink-0 items-center justify-center rounded-sm border transition-colors duration-100
					{checked ? 'border-[var(--color-accent)] bg-[var(--color-accent)]' : 'border-[var(--color-border-mid)] bg-[var(--color-bg-4)]'}">
					{#if checked}
						<svg width="8" height="8" viewBox="0 0 24 24" fill="none" stroke="white" stroke-width="3.5" stroke-linecap="round" stroke-linejoin="round">
							<polyline points="20 6 9 17 4 12" />
						</svg>
					{/if}
				</span>
				<input type="checkbox" class="sr-only" {checked} onchange={() => toggle(et.value)} />
				<span class="font-mono text-meta {checked ? 'text-[var(--color-text-bright)]' : 'text-[var(--color-text-secondary)]'}">{et.value}</span>
			</label>
		{/each}

		{#if gi < Object.entries(groupedEvents).length - 1}
			<div class="mx-3 my-1 border-t border-[var(--color-border)]"></div>
		{/if}
	{/each}
{/snippet}

{#snippet providerIcon(provider: string)}
	{#if provider === 'discord'}
		<svg width="12" height="12" viewBox="0 0 24 24" fill="currentColor"><path d="M20.317 4.37a19.791 19.791 0 0 0-4.885-1.515.074.074 0 0 0-.079.037c-.21.375-.444.864-.608 1.25a18.27 18.27 0 0 0-5.487 0 12.64 12.64 0 0 0-.617-1.25.077.077 0 0 0-.079-.037A19.736 19.736 0 0 0 3.677 4.37a.07.07 0 0 0-.032.027C.533 9.046-.32 13.58.099 18.057a.082.082 0 0 0 .031.057 19.9 19.9 0 0 0 5.993 3.03.078.078 0 0 0 .084-.028c.462-.63.874-1.295 1.226-1.994a.076.076 0 0 0-.041-.106 13.107 13.107 0 0 1-1.872-.892.077.077 0 0 1-.008-.128 10.2 10.2 0 0 0 .372-.292.074.074 0 0 1 .077-.01c3.928 1.793 8.18 1.793 12.062 0a.074.074 0 0 1 .078.01c.12.098.246.198.373.292a.077.077 0 0 1-.006.127 12.299 12.299 0 0 1-1.873.892.077.077 0 0 0-.041.107c.36.698.772 1.362 1.225 1.993a.076.076 0 0 0 .084.028 19.839 19.839 0 0 0 6.002-3.03.077.077 0 0 0 .032-.054c.5-5.177-.838-9.674-3.549-13.66a.061.061 0 0 0-.031-.03z" /></svg>
	{:else if provider === 'slack'}
		<svg width="12" height="12" viewBox="0 0 24 24" fill="currentColor"><path d="M5.042 15.165a2.528 2.528 0 0 1-2.52 2.523A2.528 2.528 0 0 1 0 15.165a2.527 2.527 0 0 1 2.522-2.52h2.52v2.52zm1.271 0a2.527 2.527 0 0 1 2.521-2.52 2.527 2.527 0 0 1 2.521 2.52v6.313A2.528 2.528 0 0 1 8.834 24a2.528 2.528 0 0 1-2.521-2.522v-6.313zM8.834 5.042a2.528 2.528 0 0 1-2.521-2.52A2.528 2.528 0 0 1 8.834 0a2.528 2.528 0 0 1 2.521 2.522v2.52H8.834zm0 1.271a2.528 2.528 0 0 1 2.521 2.521 2.528 2.528 0 0 1-2.521 2.521H2.522A2.528 2.528 0 0 1 0 8.834a2.528 2.528 0 0 1 2.522-2.521h6.312zm10.122 2.521a2.528 2.528 0 0 1 2.522-2.521A2.528 2.528 0 0 1 24 8.834a2.528 2.528 0 0 1-2.522 2.521h-2.522V8.834zm-1.268 0a2.528 2.528 0 0 1-2.523 2.521 2.527 2.527 0 0 1-2.52-2.521V2.522A2.527 2.527 0 0 1 15.165 0a2.528 2.528 0 0 1 2.523 2.522v6.312zm-2.523 10.122a2.528 2.528 0 0 1 2.523 2.522A2.528 2.528 0 0 1 15.165 24a2.527 2.527 0 0 1-2.52-2.522v-2.522h2.52zm0-1.268a2.527 2.527 0 0 1-2.52-2.523 2.526 2.526 0 0 1 2.52-2.52h6.313A2.527 2.527 0 0 1 24 15.165a2.528 2.528 0 0 1-2.522 2.523h-6.313z" /></svg>
	{:else if provider === 'teams'}
		<svg width="12" height="12" viewBox="0 0 24 24" fill="currentColor"><path d="M20.625 8.5h-3.25V6.25a1.75 1.75 0 0 1 1.75-1.75h.002a1.75 1.75 0 1 1 0 3.5h-.002a1.7 1.7 0 0 1-.5-.074V8.5zM22.25 10h-4.375a.875.875 0 0 0-.875.875v5.25a3.375 3.375 0 0 0 3.016 3.355A3.5 3.5 0 0 0 24 16v-2.5A3.5 3.5 0 0 0 22.25 10zM9.5 7a3 3 0 1 0 0-6 3 3 0 0 0 0 6zm5.25 3H4.25A2.25 2.25 0 0 0 2 12.25V18a5.5 5.5 0 0 0 11 0v-5.75A2.25 2.25 0 0 0 14.75 10zM16 8.5a2.5 2.5 0 1 0 0-5 2.5 2.5 0 0 0 0 5z" /></svg>
	{:else if provider === 'googlechat'}
		<svg width="12" height="12" viewBox="0 0 24 24" fill="currentColor"><path d="M12 0C5.372 0 0 5.042 0 11.264c0 2.026.564 3.94 1.544 5.612L.05 21.932a.75.75 0 0 0 .96.932l5.112-1.7A12.4 12.4 0 0 0 12 22.528c6.628 0 12-5.042 12-11.264S18.628 0 12 0zm4.5 14.25h-9a.75.75 0 0 1 0-1.5h9a.75.75 0 0 1 0 1.5zm0-3h-9a.75.75 0 0 1 0-1.5h9a.75.75 0 0 1 0 1.5zm0-3h-9a.75.75 0 0 1 0-1.5h9a.75.75 0 0 1 0 1.5z" /></svg>
	{:else if provider === 'telegram'}
		<svg width="12" height="12" viewBox="0 0 24 24" fill="currentColor"><path d="M11.944 0A12 12 0 0 0 0 12a12 12 0 0 0 12 12 12 12 0 0 0 12-12A12 12 0 0 0 12 0a12 12 0 0 0-.056 0zm4.962 7.224c.1-.002.321.023.465.14a.506.506 0 0 1 .171.325c.016.093.036.306.02.472-.18 1.898-.962 6.502-1.36 8.627-.168.9-.499 1.201-.82 1.23-.696.065-1.225-.46-1.9-.902-1.056-.693-1.653-1.124-2.678-1.8-1.185-.78-.417-1.21.258-1.91.177-.184 3.247-2.977 3.307-3.23.007-.032.014-.15-.056-.212s-.174-.041-.249-.024c-.106.024-1.793 1.14-5.061 3.345-.479.33-.913.49-1.302.48-.428-.008-1.252-.241-1.865-.44-.752-.245-1.349-.374-1.297-.789.027-.216.325-.437.893-.663 3.498-1.524 5.83-2.529 6.998-3.014 3.332-1.386 4.025-1.627 4.476-1.635z" /></svg>
	{:else if provider === 'matrix'}
		<svg width="12" height="12" viewBox="0 0 24 24" fill="currentColor"><path d="M.632.55v22.9H2.28V24H0V0h2.28v.55zm7.043 7.26v1.157h.033c.309-.443.683-.784 1.117-1.024.434-.24.905-.36 1.416-.36.54 0 1.033.107 1.48.32.448.214.773.553.974 1.02.309-.4.694-.727 1.154-.98a3.1 3.1 0 0 1 1.49-.36c.434 0 .839.058 1.213.172.375.115.694.303.957.565.264.262.467.6.61 1.014.144.414.215.907.215 1.478V17.3h-2.36v-5.18c0-.312-.013-.603-.04-.873a1.84 1.84 0 0 0-.195-.685 1.08 1.08 0 0 0-.432-.45c-.187-.108-.438-.162-.754-.162-.316 0-.573.058-.77.172a1.27 1.27 0 0 0-.472.46 1.98 1.98 0 0 0-.24.672 4.4 4.4 0 0 0-.065.746V17.3H9.36v-5.07c0-.282-.006-.558-.02-.826a2.15 2.15 0 0 0-.148-.72 1.04 1.04 0 0 0-.403-.498c-.182-.126-.44-.19-.773-.19a1.55 1.55 0 0 0-.416.068c-.158.052-.31.147-.458.286a1.62 1.62 0 0 0-.36.566c-.1.238-.15.545-.15.924V17.3H4.28V7.81zm15.693 15.64V.55H21.72V0H24v24h-2.28v-.55z" /></svg>
	{:else if provider === 'webhook'}
		<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71" /><path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71" /></svg>
	{:else}
		<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="2" /><path d="M16.24 7.76a6 6 0 0 1 0 8.49" /><path d="M7.76 16.24a6 6 0 0 1 0-8.49" /></svg>
	{/if}
{/snippet}

<style>
	/* Skeleton shimmer — GPU-composited, no paint cost */
	.skeleton {
		background: linear-gradient(
			90deg,
			var(--color-bg-4) 0%,
			var(--color-bg-5) 50%,
			var(--color-bg-4) 100%
		);
		background-size: 200% 100%;
		animation: shimmer 1.6s ease-in-out infinite;
	}

	@keyframes shimmer {
		0% { background-position: 200% center; }
		100% { background-position: -200% center; }
	}

	/* Webhook secret reveal animations */
	@keyframes checkDraw {
		from { stroke-dashoffset: 24; }
		to   { stroke-dashoffset: 0; }
	}

	@keyframes circlePop {
		from { transform: scale(0); opacity: 0; }
		60%  { transform: scale(1.18); opacity: 1; }
		to   { transform: scale(1);    opacity: 1; }
	}

	@keyframes keyRevealGlow {
		0%   { border-color: var(--color-accent); box-shadow: 0 0 0 3px rgba(94,140,88,0.16); }
		60%  { border-color: var(--color-accent); box-shadow: 0 0 0 3px rgba(94,140,88,0.08); }
		100% { border-color: var(--color-border-mid); box-shadow: none; }
	}

	@keyframes copyBounce {
		0%   { transform: scale(1);    }
		40%  { transform: scale(1.12); }
		100% { transform: scale(1);    }
	}

	/* Row born flash */
	@keyframes channel-born {
		0%, 25% { background-color: rgba(94, 140, 88, 0.1); }
		100%    { background-color: transparent; }
	}
	.channel-born {
		animation: channel-born 1.6s ease-out forwards;
	}

	/* Left accent stripe — slides in on hover */
	.row-stripe {
		transform: scaleY(0);
		transform-origin: center;
		transition: transform 0.18s cubic-bezier(0.25, 1, 0.5, 1);
	}
	.channel-row:hover .row-stripe {
		transform: scaleY(1);
	}

	/* Accent-tinted row hover */
	.channel-row:hover {
		background: rgba(94, 140, 88, 0.04);
	}
</style>
