<script lang="ts">
	import Sidebar from '$lib/components/Sidebar.svelte';
	import { onMount } from 'svelte';
	import { auth } from '$lib/auth.svelte';
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

	let collapsed = $state(
		typeof window !== 'undefined'
			? localStorage.getItem('wrenn_sidebar_collapsed') === 'true'
			: false
	);

	let canManage = $derived(auth.role === 'owner' || auth.role === 'admin');

	// List state
	let hosts = $state<Host[]>([]);
	let loading = $state(true);
	let error = $state<string | null>(null);

	// Create dialog
	let showCreate = $state(false);
	let createForm = $state({ provider: '', availability_zone: '' });
	let creating = $state(false);
	let createError = $state<string | null>(null);

	// Token reveal — shown once after creation
	let createdResult = $state<CreateHostResult | null>(null);
	let tokenCopied = $state(false);
	let checkmarkVisible = $state(false);

	// Delete confirmation
	let deleteTarget = $state<Host | null>(null);
	let deletePreviewSandboxes = $state<string[]>([]);
	let deletePreviewLoading = $state(false);
	let deleting = $state(false);
	let deleteError = $state<string | null>(null);

	let flashHostId = $state<string | null>(null);
	let newHostId = $state<string | null>(null);

	// Derived stats
	let onlineCount = $derived(hosts.filter((h) => h.status === 'online').length);

	async function fetchHosts() {
		loading = true;
		error = null;
		const result = await listHosts();
		if (result.ok) {
			hosts = result.data.filter((h) => h.type === 'byoc');
		} else {
			error = result.error;
		}
		loading = false;
	}

	async function handleCreate() {
		creating = true;
		createError = null;
		const result = await createHost({
			type: 'byoc',
			team_id: auth.teamId ?? undefined,
			provider: createForm.provider.trim() || undefined,
			availability_zone: createForm.availability_zone.trim() || undefined
		});
		if (result.ok) {
			showCreate = false;
			createForm = { provider: '', availability_zone: '' };
			createdResult = result.data;
			newHostId = result.data.host.id;
			hosts = [result.data.host, ...hosts];
			flashHostId = result.data.host.id;
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
		deletePreviewSandboxes = [];
		deletePreviewLoading = true;
		const preview = await getDeletePreview(host.id);
		deletePreviewLoading = false;
		if (preview.ok) {
			deletePreviewSandboxes = preview.data.sandbox_ids;
		}
	}

	async function handleDelete() {
		if (!deleteTarget) return;
		deleting = true;
		deleteError = null;
		const result = await deleteHost(deleteTarget.id, deletePreviewSandboxes.length > 0);
		if (result.ok) {
			hosts = hosts.filter((h) => h.id !== deleteTarget!.id);
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

<div class="flex h-screen overflow-hidden bg-[var(--color-bg-0)]">
	<Sidebar bind:collapsed />

	<main class="flex min-w-0 flex-1 flex-col overflow-hidden">
		<!-- Header -->
		<header class="flex shrink-0 flex-col gap-4 border-b border-[var(--color-border)] bg-[var(--color-bg-1)] px-6 py-5">
			<div class="flex items-start justify-between">
				<div>
					<h1 class="font-serif text-[1.75rem] leading-none tracking-[-0.03em] text-[var(--color-text-bright)]">
						BYOC Hosts
					</h1>
					<p class="mt-1.5 text-ui text-[var(--color-text-tertiary)]">
						Your own compute, running Wrenn capsules.
					</p>
				</div>
				{#if canManage}
					<button
						onclick={() => { showCreate = true; createError = null; createForm = { provider: '', availability_zone: '' }; }}
						class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-accent)] px-4 py-2 text-ui font-semibold text-white shadow-sm transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0"
					>
						<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>
						Register Host
					</button>
				{/if}
			</div>

			<!-- Stat pills (only when data is loaded) -->
			{#if !loading && !error && hosts.length > 0}
				<div class="flex items-center gap-2">
					<div class="flex items-baseline gap-1 rounded-[var(--radius-button)] border border-[var(--color-border)] bg-[var(--color-bg-2)] px-2.5 py-1">
						<span class="font-mono font-semibold text-ui tabular-nums text-[var(--color-text-bright)]">{hosts.length}</span>
						<span class="text-label text-[var(--color-text-muted)]">total</span>
					</div>
					<div class="flex items-baseline gap-1.5 rounded-[var(--radius-button)] border border-[var(--color-accent)]/25 bg-[var(--color-accent)]/8 px-2.5 py-1">
						<span class="relative mt-px flex h-1.5 w-1.5 shrink-0 self-center">
							<span class="absolute inline-flex h-full w-full animate-ping rounded-full bg-[var(--color-accent)] opacity-60"></span>
							<span class="relative inline-flex h-1.5 w-1.5 rounded-full bg-[var(--color-accent)]"></span>
						</span>
						<span class="font-mono font-semibold text-ui tabular-nums text-[var(--color-accent-bright)]">{onlineCount}</span>
						<span class="text-label text-[var(--color-accent-bright)]/70">online</span>
					</div>
				</div>
			{/if}
		</header>

		<!-- Body -->
		<div class="flex-1 overflow-y-auto p-6">
			{#if loading}
				{@render skeletonRows()}
			{:else if error}
				<div class="rounded-[var(--radius-card)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-4 py-3 text-ui text-[var(--color-red)]">
					{error}
				</div>
			{:else if hosts.length === 0}
				{@render emptyState()}
			{:else}
				<div class="rounded-[var(--radius-card)] border border-[var(--color-border)] bg-[var(--color-bg-1)] overflow-hidden">
					<table class="w-full">
						<thead>
							<tr class="border-b border-[var(--color-border)]">
								<th class="px-4 py-3 text-left text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Host</th>
								<th class="px-4 py-3 text-left text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Status</th>
								<th class="hidden px-4 py-3 text-left text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)] md:table-cell">Specs</th>
								<th class="hidden px-4 py-3 text-left text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)] lg:table-cell">Last Heartbeat</th>
								<th class="hidden px-4 py-3 text-left text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)] lg:table-cell">Registered</th>
								{#if canManage}
									<th class="px-4 py-3"></th>
								{/if}
							</tr>
						</thead>
						<tbody>
							{#each hosts as host (host.id)}
								<tr
									class="border-b border-[var(--color-border)] last:border-0 transition-colors duration-200
										{host.id === newHostId ? 'new-row' : ''}
										{flashHostId === host.id ? 'bg-[var(--color-accent-glow)]' : 'hover:bg-[var(--color-bg-2)]'}"
								>
									<td class="px-4 py-3.5">
										<div class="font-mono text-meta text-[var(--color-text-primary)]">{host.id}</div>
										{#if host.address}
											<div class="mt-0.5 font-mono text-label text-[var(--color-text-muted)]">{host.address}</div>
										{/if}
										{#if host.provider || host.availability_zone}
											<div class="mt-0.5 text-label text-[var(--color-text-tertiary)]">
												{[host.provider, host.availability_zone].filter(Boolean).join(' · ')}
											</div>
										{/if}
									</td>
									<td class="px-4 py-3.5">
										<span class="flex items-center gap-1.5 text-meta font-medium" style="color: {statusColor(host.status)}">
											{#if host.status === 'online'}
												<span class="relative flex h-1.5 w-1.5 shrink-0">
													<span class="absolute inline-flex h-full w-full animate-ping rounded-full opacity-60" style="background: {statusColor(host.status)}"></span>
													<span class="relative inline-flex h-1.5 w-1.5 rounded-full" style="background: {statusColor(host.status)}"></span>
												</span>
											{:else}
												<span class="h-1.5 w-1.5 shrink-0 rounded-full" style="background: {statusColor(host.status)}"></span>
											{/if}
											{host.status}
										</span>
									</td>
									<td class="hidden px-4 py-3.5 md:table-cell">
										<span class="text-meta text-[var(--color-text-secondary)]">{formatSpecs(host)}</span>
									</td>
									<td class="hidden px-4 py-3.5 lg:table-cell">
										<span class="text-meta text-[var(--color-text-muted)]" title={host.last_heartbeat_at ? formatDate(host.last_heartbeat_at) : undefined}>
											{host.last_heartbeat_at ? timeAgo(host.last_heartbeat_at) : '—'}
										</span>
									</td>
									<td class="hidden px-4 py-3.5 lg:table-cell">
										<span class="text-meta text-[var(--color-text-muted)]" title={formatDate(host.created_at)}>
											{timeAgo(host.created_at)}
										</span>
									</td>
									{#if canManage}
										<td class="px-4 py-3.5 text-right">
											<button
												onclick={() => openDeleteConfirm(host)}
												class="rounded-[var(--radius-button)] px-3 py-1.5 text-meta text-[var(--color-text-tertiary)] transition-colors duration-150 hover:bg-[var(--color-red)]/10 hover:text-[var(--color-red)]"
											>
												Delete
											</button>
										</td>
									{/if}
								</tr>
							{/each}
						</tbody>
					</table>
				</div>
			{/if}
		</div>
	</main>
</div>

{#snippet skeletonRows()}
	<div class="rounded-[var(--radius-card)] border border-[var(--color-border)] bg-[var(--color-bg-1)] overflow-hidden">
		<table class="w-full">
			<thead>
				<tr class="border-b border-[var(--color-border)]">
					<th class="px-4 py-3 text-left text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Host</th>
					<th class="px-4 py-3 text-left text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Status</th>
					<th class="hidden px-4 py-3 md:table-cell text-left text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Specs</th>
					<th class="hidden px-4 py-3 lg:table-cell text-left text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Last Heartbeat</th>
					<th class="hidden px-4 py-3 lg:table-cell text-left text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Registered</th>
				</tr>
			</thead>
			<tbody>
				{#each Array(4) as _, i}
					<tr class="border-b border-[var(--color-border)] last:border-0" style="animation-delay: {i * 60}ms">
						<td class="px-4 py-3.5">
							<div class="skeleton mb-1.5 h-3 w-28 rounded"></div>
							<div class="skeleton h-2.5 w-20 rounded"></div>
						</td>
						<td class="px-4 py-3.5">
							<div class="skeleton h-3 w-16 rounded-full"></div>
						</td>
						<td class="hidden px-4 py-3.5 md:table-cell">
							<div class="skeleton h-3 w-24 rounded"></div>
						</td>
						<td class="hidden px-4 py-3.5 lg:table-cell">
							<div class="skeleton h-3 w-20 rounded"></div>
						</td>
						<td class="hidden px-4 py-3.5 lg:table-cell">
							<div class="skeleton h-3 w-16 rounded"></div>
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
	</div>
{/snippet}

{#snippet emptyState()}
	<div class="flex flex-col items-center justify-center py-24 text-center">
		<div class="mb-5 flex h-16 w-16 items-center justify-center rounded-[var(--radius-card)] border border-[var(--color-border)] bg-[var(--color-bg-2)]">
			<svg width="28" height="28" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.25" stroke-linecap="round" stroke-linejoin="round" class="text-[var(--color-text-muted)]"><rect x="2" y="2" width="20" height="8" rx="2"/><rect x="2" y="14" width="20" height="8" rx="2"/><line x1="6" y1="6" x2="6.01" y2="6"/><line x1="6" y1="18" x2="6.01" y2="18"/></svg>
		</div>
		{#if canManage}
			<p class="font-serif text-[1.125rem] leading-snug text-[var(--color-text-secondary)]">
				No hosts yet.
			</p>
			<p class="mt-1.5 max-w-[340px] text-ui text-[var(--color-text-muted)]">
				Register a server and Wrenn will schedule capsules on your own infrastructure.
			</p>
			<button
				onclick={() => { showCreate = true; createError = null; }}
				class="mt-6 flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-accent)] px-4 py-2 text-ui font-semibold text-white shadow-sm transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0"
			>
				<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>
				Register your first host
			</button>
		{:else}
			<p class="font-serif text-[1.125rem] leading-snug text-[var(--color-text-secondary)]">
				No hosts registered.
			</p>
			<p class="mt-1.5 max-w-[320px] text-ui text-[var(--color-text-muted)]">
				Ask a team owner or admin to register a BYOC host for your team.
			</p>
		{/if}
	</div>
{/snippet}

<!-- Register Host Dialog -->
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
			class="relative w-full max-w-[420px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] p-6 shadow-xl"
			style="animation: fadeUp 0.18s cubic-bezier(0.25,1,0.5,1) both"
		>
			<h2 class="font-serif text-[1.375rem] leading-tight tracking-[-0.02em] text-[var(--color-text-bright)]">
				Register Host
			</h2>
			<p class="mt-1.5 text-ui text-[var(--color-text-tertiary)]">
				Add a server to your team's BYOC pool. You'll receive a one-time registration token.
			</p>

			{#if createError}
				<div class="mt-4 rounded-[var(--radius-input)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-3 py-2 text-meta text-[var(--color-red)]">
					{createError}
				</div>
			{/if}

			<div class="mt-5 space-y-4">
				<div>
					<label class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]" for="byoc-provider">
						Provider <span class="normal-case font-normal text-[var(--color-text-muted)]">(optional)</span>
					</label>
					<input
						id="byoc-provider"
						type="text"
						placeholder="e.g. aws, gcp, bare-metal"
						bind:value={createForm.provider}
						disabled={creating}
						class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-4)] px-3 py-2 text-ui text-[var(--color-text-bright)] outline-none placeholder:text-[var(--color-text-muted)] transition-colors duration-150 focus:border-[var(--color-accent)] disabled:opacity-60"
					/>
				</div>
				<div>
					<label class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]" for="byoc-az">
						Availability Zone <span class="normal-case font-normal text-[var(--color-text-muted)]">(optional)</span>
					</label>
					<input
						id="byoc-az"
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
					onclick={handleCreate}
					disabled={creating}
					class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-accent)] px-5 py-2 text-ui font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0 disabled:opacity-50 disabled:hover:translate-y-0"
				>
					{#if creating}
						<svg class="animate-spin" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 12a9 9 0 1 1-6.219-8.56"/></svg>
						Registering…
					{:else}
						Register
					{/if}
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Registration Token Reveal -->
{#if createdResult}
	<div class="fixed inset-0 z-50 flex items-center justify-center">
		<div class="absolute inset-0 bg-black/60"></div>
		<div
			class="relative w-full max-w-[500px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] p-6 shadow-xl"
			style="animation: fadeUp 0.18s cubic-bezier(0.25,1,0.5,1) both"
		>
			<!-- Animated checkmark -->
			<div class="mb-5 flex h-12 w-12 items-center justify-center rounded-full bg-[var(--color-accent-glow)]">
				<svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="var(--color-accent-bright)" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
					<polyline
						points="20 6 9 17 4 12"
						class="checkmark-path"
						class:checkmark-drawn={checkmarkVisible}
					/>
				</svg>
			</div>

			<h2 class="font-serif text-[1.375rem] leading-tight tracking-[-0.02em] text-[var(--color-text-bright)]">
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
					class="w-full rounded-[var(--radius-button)] bg-[var(--color-bg-4)] px-4 py-2.5 text-ui font-medium text-[var(--color-text-primary)] transition-colors duration-150 hover:bg-[var(--color-bg-5)]"
				>
					Done
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Delete Confirmation Dialog -->
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
			class="relative w-full max-w-[420px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] p-6 shadow-xl"
			style="animation: fadeUp 0.18s cubic-bezier(0.25,1,0.5,1) both"
		>
			<h2 class="font-serif text-[1.375rem] leading-tight tracking-[-0.02em] text-[var(--color-text-bright)]">
				Delete Host
			</h2>
			<p class="mt-1.5 text-ui text-[var(--color-text-tertiary)]">
				Remove <code class="rounded bg-[var(--color-bg-4)] px-1.5 py-0.5 font-mono text-[0.8rem] text-[var(--color-text-primary)]">{deleteTarget.id}</code> from your BYOC pool.
			</p>

			{#if deletePreviewLoading}
				<div class="mt-4 flex items-center gap-2 text-meta text-[var(--color-text-muted)]">
					<svg class="animate-spin" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 12a9 9 0 1 1-6.219-8.56"/></svg>
					Checking active capsules…
				</div>
			{:else if deletePreviewSandboxes.length > 0}
				<div class="mt-4 rounded-[var(--radius-input)] border border-[var(--color-amber)]/30 bg-[var(--color-amber)]/6 px-3 py-2.5">
					<p class="text-meta font-semibold text-[var(--color-amber)]">
						{deletePreviewSandboxes.length} active capsule{deletePreviewSandboxes.length === 1 ? '' : 's'} will be destroyed.
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
					class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-red)] px-5 py-2 text-ui font-semibold text-white transition-all duration-150 hover:brightness-110 disabled:opacity-50"
				>
					{#if deleting}
						<svg class="animate-spin" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 12a9 9 0 1 1-6.219-8.56"/></svg>
						Deleting…
					{:else}
						{deletePreviewSandboxes.length > 0 ? 'Force Delete' : 'Delete'}
					{/if}
				</button>
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
</style>
