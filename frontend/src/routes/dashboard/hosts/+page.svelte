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
	let initialAnimDone = $state(false);

	// Create dialog
	let showCreate = $state(false);
	let createForm = $state({ provider: '', availability_zone: '' });
	let creating = $state(false);
	let createError = $state<string | null>(null);

	// Token reveal — shown once after creation
	let createdResult = $state<CreateHostResult | null>(null);
	let tokenCopied = $state(false);
	let copyCount = $state(0);
	let checkmarkVisible = $state(false);

	// Delete confirmation
	let deleteTarget = $state<Host | null>(null);
	let deletePreviewCapsules = $state<string[]>([]);
	let deletePreviewLoading = $state(false);
	let deleting = $state(false);
	let deleteError = $state<string | null>(null);

	let flashHostId = $state<string | null>(null);
	let newHostId = $state<string | null>(null);

	// Derived stats
	let onlineCount = $derived(hosts.filter((h) => h.status === 'online').length);
	let offlineCount = $derived(hosts.filter((h) => h.status === 'offline' || h.status === 'unreachable').length);

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
		if (!initialAnimDone) {
			requestAnimationFrame(() => { initialAnimDone = true; });
		}
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
			hosts = hosts.filter((h) => h.id !== deleteTarget!.id);
			deleteTarget = null;
			toast.success('Host deleted');
		} else {
			deleteError = result.error;
		}
		deleting = false;
	}

	async function copyToken(token: string) {
		try {
			await navigator.clipboard.writeText(token);
			tokenCopied = true;
			copyCount += 1;
			setTimeout(() => (tokenCopied = false), 2000);
		} catch {
			toast.error('Copy failed — select the token and copy manually.');
		}
	}

	function closeTokenReveal() {
		createdResult = null;
		checkmarkVisible = false;
		newHostId = null;
	}

	function statusLabel(status: Host['status']): string {
		switch (status) {
			case 'online': return 'Online';
			case 'pending': return 'Pending';
			case 'offline': return 'Offline';
			case 'unreachable': return 'Unreachable';
			case 'draining': return 'Draining';
			default: return status;
		}
	}

	onMount(fetchHosts);
</script>

<svelte:head>
	<title>Wrenn — Hosts</title>
</svelte:head>

<div class="flex h-screen overflow-hidden">
	<Sidebar bind:collapsed />

	<div class="flex flex-1 flex-col overflow-hidden">
		<main class="flex-1 overflow-y-auto bg-[var(--color-bg-0)]">
			<!-- Header -->
			<div class="px-7 pt-8">
				<div class="flex items-center justify-between">
					<div>
						<h1 class="font-serif text-page text-[var(--color-text-bright)]">
							Hosts
						</h1>
						<p class="mt-2 text-ui text-[var(--color-text-secondary)]">
							Your own compute, running Wrenn capsules.
						</p>
					</div>

					{#if canManage}
						<button
							onclick={() => { showCreate = true; createError = null; createForm = { provider: '', availability_zone: '' }; }}
							class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-accent)] px-4 py-2 text-ui font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0"
						>
							<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>
							Register Host
						</button>
					{/if}
				</div>

				<!-- Stat pills — staggered entrance -->
				{#if !loading && !error && hosts.length > 0}
					<div class="mt-4 flex items-center gap-2.5">
						<div class="stat-pill flex items-baseline gap-1.5 rounded-[var(--radius-button)] border border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 py-1.5" style="animation-delay: 0ms">
							<span class="font-mono text-body font-bold tabular-nums text-[var(--color-text-bright)]">{hosts.length}</span>
							<span class="text-label font-medium uppercase tracking-[0.04em] text-[var(--color-text-muted)]">total</span>
						</div>
						<div class="stat-pill flex items-center gap-2 rounded-[var(--radius-button)] border border-[var(--color-accent)]/25 bg-[var(--color-accent)]/[0.06] px-3 py-1.5" style="animation-delay: 60ms">
							<span class="relative flex h-[7px] w-[7px] shrink-0">
								<span class="absolute inline-flex h-full w-full rounded-full bg-[var(--color-accent)] opacity-50" style="animation: statusPulse 2s ease-in-out infinite"></span>
								<span class="relative inline-flex h-[7px] w-[7px] rounded-full bg-[var(--color-accent)]"></span>
							</span>
							<span class="font-mono text-body font-bold tabular-nums text-[var(--color-accent-bright)]">{onlineCount}</span>
							<span class="text-label font-medium uppercase tracking-[0.04em] text-[var(--color-accent-mid)]">online</span>
						</div>
						{#if offlineCount > 0}
							<div class="stat-pill flex items-center gap-2 rounded-[var(--radius-button)] border border-[var(--color-red)]/20 bg-[var(--color-red)]/[0.04] px-3 py-1.5" style="animation-delay: 120ms">
								<span class="h-[7px] w-[7px] shrink-0 rounded-full bg-[var(--color-red)]/60"></span>
								<span class="font-mono text-body font-bold tabular-nums text-[var(--color-red)]">{offlineCount}</span>
								<span class="text-label font-medium uppercase tracking-[0.04em] text-[var(--color-red)]/60">offline</span>
							</div>
						{/if}
					</div>
				{/if}

				<div class="mt-6 border-b border-[var(--color-border)]"></div>
			</div>

			<!-- Content -->
			<div class="p-8" style="animation: fadeUp 0.35s ease both">
				{#if error}
					<div class="mb-4 flex items-center justify-between gap-4 rounded-[var(--radius-card)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-4 py-3 text-ui text-[var(--color-red)]">
						<span>{error}</span>
						<button onclick={fetchHosts} class="shrink-0 font-semibold underline-offset-2 hover:underline">
							Try again
						</button>
					</div>
				{/if}

				{#if loading}
					{@render skeletonRows()}
				{:else if hosts.length === 0}
					{@render emptyState()}
				{:else}
					<div class="rounded-[var(--radius-card)] border border-[var(--color-border)] overflow-hidden">
						<!-- Table header -->
						<div class="grid host-grid border-b border-[var(--color-border)] bg-[var(--color-bg-3)]">
							<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Host</div>
							<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Status</div>
							<div class="hidden px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)] md:block">Specs</div>
							<div class="hidden px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)] lg:block">Last Heartbeat</div>
							<div class="hidden px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)] lg:block">Registered</div>
							{#if canManage}
								<div class="px-5 py-3"></div>
							{/if}
						</div>

						<!-- Table rows -->
						{#each hosts as host, i (host.id)}
							<div
								class="host-row relative grid host-grid items-center overflow-hidden border-b border-[var(--color-border)] transition-colors duration-150 hover:bg-[var(--color-bg-3)] last:border-b-0
									{host.id === newHostId ? 'new-row' : ''}
									{flashHostId === host.id ? 'host-born' : ''}"
								style={initialAnimDone ? undefined : `animation: fadeUp 0.35s ease both; animation-delay: ${i * 40}ms`}
							>
								<!-- Accent stripe -->
								<div class="row-stripe pointer-events-none absolute left-0 top-0 h-full w-0.5" style="background: {statusColor(host.status)}"></div>

								<!-- Host identity -->
								<div class="min-w-0 px-5 py-4">
									<span class="font-mono text-ui font-medium text-[var(--color-text-bright)]">{host.id}</span>
									{#if host.address}
										<div class="mt-0.5 font-mono text-label text-[var(--color-text-muted)]">{host.address}</div>
									{/if}
									{#if host.provider || host.availability_zone}
										<div class="mt-1 flex items-center gap-1.5">
											{#if host.provider}
												<span class="inline-flex items-center rounded-sm border border-[var(--color-border-mid)] bg-[var(--color-bg-4)] px-1.5 py-0.5 font-mono text-badge text-[var(--color-text-tertiary)]">{host.provider}</span>
											{/if}
											{#if host.availability_zone}
												<span class="inline-flex items-center rounded-sm border border-[var(--color-border-mid)] bg-[var(--color-bg-4)] px-1.5 py-0.5 font-mono text-badge text-[var(--color-text-tertiary)]">{host.availability_zone}</span>
											{/if}
										</div>
									{/if}
								</div>

								<!-- Status -->
								<div class="px-5 py-4">
									<span class="inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-meta font-medium"
										style="color: {statusColor(host.status)}; background: color-mix(in srgb, {statusColor(host.status)} 8%, transparent)"
									>
										{#if host.status === 'online'}
											<span class="relative flex h-[6px] w-[6px] shrink-0">
												<span class="absolute inline-flex h-full w-full rounded-full opacity-50" style="background: {statusColor(host.status)}; animation: statusPulse 2s ease-in-out infinite"></span>
												<span class="relative inline-flex h-[6px] w-[6px] rounded-full" style="background: {statusColor(host.status)}"></span>
											</span>
										{:else}
											<span class="h-[6px] w-[6px] shrink-0 rounded-full" style="background: {statusColor(host.status)}"></span>
										{/if}
										{statusLabel(host.status)}
									</span>
								</div>

								<!-- Specs -->
								<div class="hidden px-5 py-4 md:block">
									<span class="font-mono text-meta tabular-nums text-[var(--color-text-secondary)]">{formatSpecs(host)}</span>
								</div>

								<!-- Last heartbeat -->
								<div class="hidden px-5 py-4 lg:block">
									<span class="text-meta text-[var(--color-text-muted)]" title={host.last_heartbeat_at ? formatDate(host.last_heartbeat_at) : undefined}>
										{host.last_heartbeat_at ? timeAgo(host.last_heartbeat_at) : '—'}
									</span>
								</div>

								<!-- Registered -->
								<div class="hidden px-5 py-4 lg:block">
									<span class="text-meta text-[var(--color-text-muted)]" title={formatDate(host.created_at)}>
										{timeAgo(host.created_at)}
									</span>
								</div>

								<!-- Actions -->
								{#if canManage}
									<div class="flex justify-end px-5 py-4">
										<button
											onclick={() => openDeleteConfirm(host)}
											class="rounded-[var(--radius-button)] border border-[var(--color-border)] px-2.5 py-1 text-label font-semibold uppercase tracking-[0.04em] text-[var(--color-text-tertiary)] transition-colors duration-150 hover:border-[var(--color-red)]/40 hover:text-[var(--color-red)]"
										>
											Delete
										</button>
									</div>
								{/if}
							</div>
						{/each}
					</div>

					<p class="mt-3 text-meta text-[var(--color-text-muted)]">
						{hosts.length} {hosts.length === 1 ? 'host' : 'hosts'} registered
					</p>
				{/if}
			</div>
		</main>

		<footer class="h-px shrink-0 bg-[var(--color-border)]"></footer>
	</div>
</div>

{#snippet skeletonRows()}
	<div class="rounded-[var(--radius-card)] border border-[var(--color-border)] overflow-hidden">
		<div class="grid host-grid border-b border-[var(--color-border)] bg-[var(--color-bg-3)]">
			<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Host</div>
			<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Status</div>
			<div class="hidden px-5 py-3 md:block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Specs</div>
			<div class="hidden px-5 py-3 lg:block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Last Heartbeat</div>
			<div class="hidden px-5 py-3 lg:block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Registered</div>
		</div>
		{#each Array(4) as _, i}
			<div class="grid host-grid items-center border-b border-[var(--color-border)] last:border-b-0" style="animation: fadeUp 0.35s ease both; animation-delay: {i * 60}ms">
				<div class="px-5 py-4">
					<div class="skeleton mb-2 h-3.5 w-24 rounded"></div>
					<div class="skeleton h-2.5 w-16 rounded"></div>
				</div>
				<div class="px-5 py-4">
					<div class="skeleton h-5 w-16 rounded-full"></div>
				</div>
				<div class="hidden px-5 py-4 md:block">
					<div class="skeleton h-3 w-28 rounded"></div>
				</div>
				<div class="hidden px-5 py-4 lg:block">
					<div class="skeleton h-3 w-16 rounded"></div>
				</div>
				<div class="hidden px-5 py-4 lg:block">
					<div class="skeleton h-3 w-14 rounded"></div>
				</div>
			</div>
		{/each}
	</div>
{/snippet}

{#snippet emptyState()}
	<div class="flex flex-col items-center justify-center py-[72px]">
		<div class="relative mb-5">
			<div class="absolute inset-0 -m-4 rounded-full" style="background: radial-gradient(circle, rgba(94,140,88,0.08) 0%, transparent 70%)"></div>
			<div class="relative flex h-14 w-14 items-center justify-center rounded-[var(--radius-card)] border border-[var(--color-accent)]/20 bg-[var(--color-bg-3)]" style="animation: iconFloat 4s ease-in-out infinite">
				<svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="var(--color-accent-mid)" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
					<rect x="2" y="2" width="20" height="8" rx="2"/>
					<rect x="2" y="14" width="20" height="8" rx="2"/>
					<line x1="6" y1="6" x2="6.01" y2="6"/>
					<line x1="6" y1="18" x2="6.01" y2="18"/>
				</svg>
			</div>
		</div>
		{#if canManage}
			<p class="font-serif text-heading text-[var(--color-text-bright)]">No hosts yet</p>
			<p class="mt-1.5 max-w-[340px] text-center text-ui text-[var(--color-text-tertiary)]">
				Register a server and Wrenn will schedule capsules on your own infrastructure.
			</p>
			<button
				onclick={() => { showCreate = true; createError = null; }}
				class="mt-6 flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-accent)] px-5 py-2.5 text-ui font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0"
			>
				Register your first host
				<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
					<line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/>
				</svg>
			</button>
		{:else}
			<p class="font-serif text-heading text-[var(--color-text-bright)]">No hosts registered</p>
			<p class="mt-1.5 max-w-[320px] text-center text-ui text-[var(--color-text-tertiary)]">
				Ask a team owner or admin to register a host for your team.
			</p>
		{/if}
	</div>
{/snippet}

<!-- Register Host Dialog -->
{#if showCreate}
	<div class="fixed inset-0 z-50 flex items-center justify-center">
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="absolute inset-0 bg-black/60"
			onclick={() => { if (!creating) showCreate = false; }}
			onkeydown={(e) => { if (e.key === 'Escape' && !creating) showCreate = false; }}
		></div>

		<div class="relative w-full max-w-[420px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] p-6" style="animation: fadeUp 0.2s ease both; box-shadow: var(--shadow-dialog)">
			<h2 class="font-serif text-heading text-[var(--color-text-bright)]">Register Host</h2>
			<p class="mt-1 text-ui text-[var(--color-text-tertiary)]">
				Add a server to your team's host pool. You'll receive a one-time registration token.
			</p>

			{#if createError}
				<div class="mt-4 rounded-[var(--radius-input)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-3 py-2 text-meta text-[var(--color-red)]">
					{createError}
				</div>
			{/if}

			<div class="mt-5 space-y-4">
				<div>
					<label class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]" for="host-provider">
						Provider <span class="normal-case font-normal text-[var(--color-text-muted)]">(optional)</span>
					</label>
					<input
						id="host-provider"
						type="text"
						placeholder="e.g. aws, gcp, bare-metal"
						bind:value={createForm.provider}
						disabled={creating}
						class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-4)] px-3 py-2 text-ui text-[var(--color-text-bright)] outline-none placeholder:text-[var(--color-text-muted)] transition-colors duration-150 focus:border-[var(--color-accent)] disabled:opacity-60"
					/>
				</div>
				<div>
					<label class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]" for="host-az">
						Availability Zone <span class="normal-case font-normal text-[var(--color-text-muted)]">(optional)</span>
					</label>
					<input
						id="host-az"
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
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="absolute inset-0 bg-black/60"
			onclick={closeTokenReveal}
			onkeydown={(e) => { if (e.key === 'Escape') closeTokenReveal(); }}
		></div>

		<div class="relative w-full max-w-[500px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] p-6" style="animation: fadeUp 0.2s ease both; box-shadow: var(--shadow-dialog)">
			<!-- Success indicator -->
			<div class="mb-4 flex items-center gap-2.5">
				<span class="flex h-5 w-5 shrink-0 items-center justify-center rounded-full bg-[var(--color-accent-glow-mid)]" style="animation: circlePop 0.4s cubic-bezier(0.34, 1.56, 0.64, 1) both">
					<svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="var(--color-accent-bright)" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
						<polyline
							points="20 6 9 17 4 12"
							class="checkmark-path"
							class:checkmark-drawn={checkmarkVisible}
						/>
					</svg>
				</span>
				<span class="text-meta font-semibold text-[var(--color-accent-mid)]" style="animation: fadeUp 0.3s 0.15s ease both">Host registered successfully</span>
			</div>

			<h2 class="font-serif text-heading text-[var(--color-text-bright)]">Registration Token</h2>
			<p class="mt-1 text-ui text-[var(--color-text-tertiary)]">
				Pass this token to the host agent to complete registration. It expires in
				<strong class="font-semibold text-[var(--color-amber)]">1 hour</strong> and is single-use.
			</p>

			<!-- Token display -->
			<div class="mt-5 rounded-[var(--radius-input)] border bg-[var(--color-bg-0)] p-4" style="animation: tokenRevealGlow 1.4s 0.1s ease-out both">
				<div class="flex items-center gap-3">
					<code class="min-w-0 flex-1 break-all font-mono text-ui leading-relaxed text-[var(--color-text-bright)]">
						{createdResult.registration_token}
					</code>
					{#key copyCount}
						<button
							onclick={() => copyToken(createdResult!.registration_token)}
							style={tokenCopied ? 'animation: copyBounce 0.35s cubic-bezier(0.34, 1.56, 0.64, 1) both' : ''}
							class="shrink-0 flex items-center gap-1.5 rounded-[var(--radius-button)] border px-3 py-1.5 text-meta font-semibold transition-all duration-150
								{tokenCopied
									? 'border-[var(--color-accent)]/40 bg-[var(--color-accent-glow-mid)] text-[var(--color-accent-mid)]'
									: 'border-[var(--color-border-mid)] text-[var(--color-text-secondary)] hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)]'}"
						>
							{#if tokenCopied}
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
					This token will not be shown again. Store it in your secrets manager — not a note, not a chat message, not a commit.
				</p>
			</div>

			<div class="mt-6 flex justify-end">
				<button
					onclick={closeTokenReveal}
					class="rounded-[var(--radius-button)] bg-[var(--color-bg-4)] border border-[var(--color-border-mid)] px-5 py-2 text-ui font-semibold text-[var(--color-text-primary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:bg-[var(--color-bg-5)]"
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
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="absolute inset-0 bg-black/60"
			onclick={() => { if (!deleting) deleteTarget = null; }}
			onkeydown={(e) => { if (e.key === 'Escape' && !deleting) deleteTarget = null; }}
		></div>

		<div class="relative w-full max-w-[380px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] p-6" style="animation: fadeUp 0.2s ease both; box-shadow: var(--shadow-dialog)">
			<h2 class="font-serif text-heading text-[var(--color-text-bright)]">Delete Host</h2>
			<p class="mt-2 text-ui text-[var(--color-text-tertiary)]">
				Remove <span class="inline-flex items-center rounded-sm border border-[var(--color-border-mid)] bg-[var(--color-bg-4)] px-1.5 py-0.5 font-mono text-badge text-[var(--color-text-primary)]">{deleteTarget.id}</span> from your host pool.
			</p>

			{#if deletePreviewLoading}
				<div class="mt-4 flex items-center gap-2 text-meta text-[var(--color-text-muted)]">
					<svg class="animate-spin" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 12a9 9 0 1 1-6.219-8.56"/></svg>
					Checking active capsules…
				</div>
			{:else if deletePreviewCapsules.length > 0}
				<div class="mt-4 rounded-[var(--radius-input)] border border-[var(--color-amber)]/20 bg-[var(--color-amber)]/5 px-3 py-2.5">
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
					class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-red)] px-5 py-2 text-ui font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0 disabled:opacity-50 disabled:hover:translate-y-0"
				>
					{#if deleting}
						<svg class="animate-spin" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 12a9 9 0 1 1-6.219-8.56"/></svg>
						Deleting…
					{:else}
						{deletePreviewCapsules.length > 0 ? 'Force Delete' : 'Delete Host'}
					{/if}
				</button>
			</div>
		</div>
	</div>
{/if}

<style>
	/* Grid layout — matches keys page pattern */
	.host-grid {
		grid-template-columns: 2fr 1fr 1.4fr 1.2fr 1fr 80px;
	}

	@media (max-width: 1023px) {
		.host-grid {
			grid-template-columns: 2fr 1fr 1.4fr 80px;
		}
	}

	@media (max-width: 767px) {
		.host-grid {
			grid-template-columns: 2fr 1fr 80px;
		}
	}

	/* Row accent stripe — slides in on hover */
	.row-stripe {
		transform: scaleY(0);
		transform-origin: center;
		transition: transform 0.18s cubic-bezier(0.25, 1, 0.5, 1);
	}
	.host-row:hover .row-stripe {
		transform: scaleY(1);
	}

	/* Born flash — new host row highlight */
	@keyframes host-born {
		0%, 25% { background-color: rgba(94, 140, 88, 0.1); }
		100%    { background-color: transparent; }
	}
	.host-born {
		animation: host-born 1.6s ease-out forwards;
	}

	/* Stat pill entrance */
	.stat-pill {
		animation: fadeUp 0.3s ease both;
	}

	/* Status dot pulse — gentler than ping */
	@keyframes statusPulse {
		0%, 100% { transform: scale(1); opacity: 0.5; }
		50%      { transform: scale(1.8); opacity: 0; }
	}

	/* Token reveal glow */
	@keyframes tokenRevealGlow {
		0%   { border-color: var(--color-accent); box-shadow: 0 0 0 3px rgba(94,140,88,0.16); }
		60%  { border-color: var(--color-accent); box-shadow: 0 0 0 3px rgba(94,140,88,0.08); }
		100% { border-color: var(--color-border-mid); box-shadow: none; }
	}

	/* Copy button bounce */
	@keyframes copyBounce {
		0%   { transform: scale(1); }
		40%  { transform: scale(1.12); }
		100% { transform: scale(1); }
	}

	/* Success circle pop */
	@keyframes circlePop {
		from { transform: scale(0); opacity: 0; }
		60%  { transform: scale(1.18); opacity: 1; }
		to   { transform: scale(1);    opacity: 1; }
	}

	/* Checkmark stroke draw */
	.checkmark-path {
		stroke-dasharray: 24;
		stroke-dashoffset: 24;
	}
	.checkmark-drawn {
		stroke-dashoffset: 0;
		transition: stroke-dashoffset 0.35s ease 0.2s;
	}

	/* New row slide-in */
	.new-row {
		animation: slideIn 0.3s cubic-bezier(0.25, 1, 0.5, 1) both;
	}

	@keyframes slideIn {
		from { opacity: 0; transform: translateX(-6px); }
		to   { opacity: 1; transform: translateX(0); }
	}

	/* Shimmer skeleton */
	@keyframes shimmer {
		0%   { background-position: -200% 0; }
		100% { background-position: 200% 0; }
	}

	.skeleton {
		background: linear-gradient(90deg, var(--color-bg-3) 25%, var(--color-bg-4) 50%, var(--color-bg-3) 75%);
		background-size: 200% 100%;
		animation: shimmer 1.4s ease infinite;
	}
</style>
