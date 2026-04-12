<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { page } from '$app/stores';
	import { goto } from '$app/navigation';
	import AdminSidebar from '$lib/components/AdminSidebar.svelte';
	import TerminalTab from '$lib/components/TerminalTab.svelte';
	import FilesTab from '$lib/components/FilesTab.svelte';
	import MetricsPanel from '$lib/components/MetricsPanel.svelte';
	import { toast } from '$lib/toast.svelte';
	import {
		getAdminCapsule,
		destroyAdminCapsule,
		snapshotAdminCapsule,
	} from '$lib/api/admin-capsules';
	import type { Capsule } from '$lib/api/capsules';

	const capsuleId: string = $page.params.id ?? '';
	const API_BASE = '/api/v1/admin/capsules';

	let collapsed = $state(
		typeof window !== 'undefined'
			? localStorage.getItem('wrenn_sidebar_collapsed') === 'true'
			: false
	);

	let capsule = $state<Capsule | null>(null);
	let capsuleLoading = $state(true);
	let capsuleError = $state<string | null>(null);

	// Destroy dialog
	let showDestroy = $state(false);
	let destroying = $state(false);
	let destroyError = $state<string | null>(null);

	// Snapshot dialog
	let showSnapshot = $state(false);
	let snapshotName = $state('');
	let snapshotting = $state(false);
	let snapshotError = $state<string | null>(null);

	const metricsAvailable = $derived(
		capsule?.status === 'running' || capsule?.status === 'paused'
	);

	async function loadCapsule() {
		const result = await getAdminCapsule(capsuleId);
		if (result.ok) {
			capsule = result.data;
			capsuleError = null;
		} else {
			capsuleError = result.error;
		}
		capsuleLoading = false;
	}

	async function handleDestroy() {
		destroying = true;
		destroyError = null;
		const result = await destroyAdminCapsule(capsuleId);
		if (result.ok) {
			toast.success('Capsule destroyed');
			goto('/admin/capsules');
		} else {
			destroyError = result.error;
		}
		destroying = false;
	}

	async function handleSnapshot() {
		snapshotting = true;
		snapshotError = null;
		const result = await snapshotAdminCapsule(capsuleId, snapshotName.trim() || undefined);
		if (result.ok) {
			toast.success(`Snapshot "${result.data.name}" created`);
			goto('/admin/templates');
		} else {
			snapshotError = result.error;
		}
		snapshotting = false;
	}

	function statusColor(status: string): string {
		switch (status) {
			case 'running': return 'var(--color-accent)';
			case 'paused':  return 'var(--color-amber)';
			case 'error':   return 'var(--color-red)';
			default:        return 'var(--color-text-muted)';
		}
	}

	function statusBg(status: string): string {
		switch (status) {
			case 'running': return 'rgba(94,140,88,0.12)';
			case 'paused':  return 'rgba(212,167,60,0.12)';
			case 'error':   return 'rgba(207,129,114,0.12)';
			default:        return 'rgba(255,255,255,0.05)';
		}
	}

	function statusBorder(status: string): string {
		switch (status) {
			case 'running': return 'rgba(94,140,88,0.3)';
			case 'paused':  return 'rgba(212,167,60,0.3)';
			case 'error':   return 'rgba(207,129,114,0.3)';
			default:        return 'rgba(255,255,255,0.08)';
		}
	}

	let pollTimer: ReturnType<typeof setInterval> | null = null;

	onMount(() => {
		loadCapsule();
		pollTimer = setInterval(loadCapsule, 10_000);
	});

	onDestroy(() => {
		if (pollTimer) clearInterval(pollTimer);
	});
</script>

<svelte:head>
	<title>Wrenn Admin — {capsuleId}</title>
</svelte:head>

<div class="flex h-screen overflow-hidden bg-[var(--color-bg-0)]">
	<AdminSidebar bind:collapsed />

	<main class="flex min-w-0 flex-1 flex-col overflow-hidden">
		{#if capsuleLoading}
			<div class="flex flex-1 items-center justify-center">
				<div class="flex items-center gap-3 text-ui text-[var(--color-text-secondary)]">
					<svg class="animate-spin" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 12a9 9 0 1 1-6.219-8.56" /></svg>
					Loading capsule...
				</div>
			</div>
		{:else if capsuleError}
			<div class="p-8">
				<div class="flex items-center gap-3 rounded-[var(--radius-card)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/8 px-5 py-4">
					<svg class="shrink-0 text-[var(--color-red)]" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
						<circle cx="12" cy="12" r="10" /><line x1="12" y1="8" x2="12" y2="12" /><line x1="12" y1="16" x2="12.01" y2="16" />
					</svg>
					<span class="text-ui text-[var(--color-red)]">{capsuleError}</span>
				</div>
			</div>
		{:else if capsule}
			<!-- Header bar -->
			<div class="flex shrink-0 items-center gap-4 border-b border-[var(--color-border)] bg-[var(--color-bg-1)] px-6 py-2.5">
				<a
					href="/admin/capsules"
					class="flex items-center gap-1.5 text-meta text-[var(--color-text-tertiary)] transition-colors duration-150 hover:text-[var(--color-text-secondary)]"
				>
					<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="15 18 9 12 15 6"/></svg>
					Capsules
				</a>
				<div class="h-4 w-px bg-[var(--color-border)]"></div>
				<span class="font-mono text-ui text-[var(--color-text-bright)]">{capsuleId}</span>
				<span
					class="inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-badge font-semibold uppercase tracking-[0.05em]"
					style="color: {statusColor(capsule.status)}; background: {statusBg(capsule.status)}; border: 1px solid {statusBorder(capsule.status)}"
				>
					{#if capsule.status === 'running'}
						<span class="relative flex h-[5px] w-[5px] shrink-0">
							<span class="animate-status-ping absolute inline-flex h-full w-full rounded-full bg-[var(--color-accent)]"></span>
							<span class="relative inline-flex h-[5px] w-[5px] rounded-full bg-[var(--color-accent)]"></span>
						</span>
					{/if}
					{capsule.status}
				</span>
				<div class="flex items-center gap-2 text-badge text-[var(--color-text-muted)]">
					<span class="font-mono">{capsule.template}</span>
					<span class="text-[var(--color-border-mid)]">/</span>
					<span class="font-mono">{capsule.vcpus}v · {capsule.memory_mb}MB</span>
				</div>
				<div class="flex-1"></div>

				{#if capsule.status === 'running' || capsule.status === 'paused'}
					<button
						onclick={() => { showSnapshot = true; snapshotName = ''; snapshotError = null; }}
						disabled={snapshotting}
						class="flex items-center gap-1.5 rounded-[var(--radius-button)] border border-[var(--color-accent)]/30 bg-[var(--color-accent)]/8 px-3 py-1.5 text-meta font-medium text-[var(--color-accent-bright)] transition-all duration-150 hover:bg-[var(--color-accent)]/15 hover:border-[var(--color-accent)]/50 disabled:opacity-50"
					>
						<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round"><path d="M14.5 4h-5L7 7H2v13a2 2 0 002 2h16a2 2 0 002-2V7h-5l-2.5-3z" /><circle cx="12" cy="15" r="3" /></svg>
						Snapshot
					</button>
					<button
						onclick={() => { showDestroy = true; destroyError = null; }}
						disabled={destroying}
						class="flex items-center gap-1.5 rounded-[var(--radius-button)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/8 px-3 py-1.5 text-meta font-medium text-[var(--color-red)] transition-all duration-150 hover:bg-[var(--color-red)]/15 hover:border-[var(--color-red)]/50 disabled:opacity-50"
					>
						<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="3 6 5 6 21 6" /><path d="M19 6v14a2 2 0 01-2 2H7a2 2 0 01-2-2V6m3 0V4a2 2 0 012-2h4a2 2 0 012 2v2" /></svg>
						Destroy
					</button>
				{/if}
			</div>

			<!-- Split panels: 50/50 -->
			<div class="flex flex-1 overflow-hidden">
				<!-- Left: Terminal -->
				<div class="flex w-1/2 flex-col overflow-hidden border-r border-[var(--color-border)]">
					<TerminalTab {capsuleId} isRunning={capsule.status === 'running'} apiBasePath={API_BASE} />
				</div>

				<!-- Right: Metrics (top 50%) + Files (bottom 50%) -->
				<div class="flex w-1/2 flex-col overflow-hidden">
					{#if metricsAvailable}
						<div class="flex flex-1 flex-col min-h-0 border-b border-[var(--color-border)]">
							<MetricsPanel {capsuleId} available={metricsAvailable} initialRange="5m" apiBasePath={API_BASE} layout="compact" />
						</div>
					{/if}

					<div class="flex flex-1 flex-col min-h-0 overflow-hidden">
						<FilesTab {capsuleId} isRunning={capsule.status === 'running'} apiBasePath={API_BASE} compact />
					</div>
				</div>
			</div>
		{/if}
	</main>
</div>

<!-- Snapshot dialog -->
{#if showSnapshot}
	<div class="fixed inset-0 z-50 flex items-center justify-center">
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="absolute inset-0 bg-black/60"
			onclick={() => { if (!snapshotting) showSnapshot = false; }}
			onkeydown={(e) => { if (e.key === 'Escape' && !snapshotting) showSnapshot = false; }}
		></div>

		<div class="relative w-full max-w-[420px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] overflow-hidden" style="animation: fadeUp 0.2s ease both; box-shadow: var(--shadow-dialog)">
			<div class="flex items-center gap-4 border-b border-[var(--color-border)] bg-[var(--color-bg-3)] px-6 py-5">
				<div class="flex h-10 w-10 shrink-0 items-center justify-center rounded-[var(--radius-input)] bg-[var(--color-accent)]/15 text-[var(--color-accent)] shadow-[0_0_12px_var(--color-accent-glow)]">
					<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round">
						<path d="M14.5 4h-5L7 7H2v13a2 2 0 002 2h16a2 2 0 002-2V7h-5l-2.5-3z" />
						<circle cx="12" cy="15" r="3" />
					</svg>
				</div>
				<div>
					<h2 class="font-serif text-heading tracking-[-0.02em] text-[var(--color-text-bright)]">Snapshot as platform template</h2>
					<p class="mt-0.5 text-meta text-[var(--color-text-muted)] font-mono">{capsuleId}</p>
				</div>
			</div>

			<div class="px-6 pt-5 pb-6 space-y-4">
				<div class="flex items-start gap-2.5 rounded-[var(--radius-input)] border border-[var(--color-amber)]/25 bg-[var(--color-amber)]/8 px-3 py-2.5">
					<svg class="mt-px shrink-0 text-[var(--color-amber)]" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
						<path d="M10.29 3.86L1.82 18a2 2 0 001.71 3h16.94a2 2 0 001.71-3L13.71 3.86a2 2 0 00-3.42 0z" />
						<line x1="12" y1="9" x2="12" y2="13" />
						<line x1="12" y1="17" x2="12.01" y2="17" />
					</svg>
					<p class="text-meta text-[var(--color-amber)] leading-relaxed">This will <strong class="font-semibold">pause, snapshot, and destroy</strong> the capsule. The snapshot will be available as a platform template for all teams.</p>
				</div>

				{#if snapshotError}
					<div class="rounded-[var(--radius-input)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-3 py-2 text-meta text-[var(--color-red)]">
						{snapshotError}
					</div>
				{/if}

				<div>
					<div class="mb-1.5 flex items-baseline justify-between">
						<label class="text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]" for="admin-snapshot-name">Template name</label>
						<span class="text-meta text-[var(--color-text-muted)]">optional</span>
					</div>
					<input
						id="admin-snapshot-name"
						type="text"
						bind:value={snapshotName}
						disabled={snapshotting}
						class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-4)] px-3 py-2 font-mono text-ui text-[var(--color-text-bright)] outline-none placeholder:text-[var(--color-text-muted)] transition-colors duration-150 focus:border-[var(--color-accent)] disabled:opacity-50"
						placeholder="e.g. python-3.12, node-22-dev"
						onkeydown={(e) => { if (e.key === 'Enter' && !snapshotting) handleSnapshot(); }}
					/>
					<p class="mt-1.5 text-meta text-[var(--color-text-muted)]">Leave blank for an auto-generated name. If the name already exists, it will be overwritten.</p>
				</div>

				<div class="flex justify-end gap-3 pt-1">
					<button
						onclick={() => { showSnapshot = false; }}
						disabled={snapshotting}
						class="rounded-[var(--radius-button)] border border-[var(--color-border)] px-4 py-2 text-ui text-[var(--color-text-secondary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)] disabled:opacity-50"
					>
						Cancel
					</button>
					<button
						onclick={handleSnapshot}
						disabled={snapshotting}
						class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-accent)] px-5 py-2 text-ui font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0 disabled:opacity-50 disabled:hover:translate-y-0"
					>
						{#if snapshotting}
							<svg class="animate-spin" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
								<path d="M21 12a9 9 0 1 1-6.219-8.56" />
							</svg>
							Snapshotting...
						{:else}
							Snapshot & Destroy
						{/if}
					</button>
				</div>
			</div>
		</div>
	</div>
{/if}

<!-- Destroy dialog -->
{#if showDestroy}
	<div class="fixed inset-0 z-50 flex items-center justify-center">
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="absolute inset-0 bg-black/60"
			onclick={() => { if (!destroying) showDestroy = false; }}
			onkeydown={(e) => { if (e.key === 'Escape' && !destroying) showDestroy = false; }}
		></div>
		<div class="relative w-full max-w-[380px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] p-6" style="animation: fadeUp 0.2s ease both; box-shadow: var(--shadow-dialog)">
			<h2 class="font-serif text-heading tracking-[-0.02em] text-[var(--color-text-bright)]">Destroy Capsule</h2>
			<p class="mt-2 text-ui text-[var(--color-text-tertiary)]">
				Terminate <span class="font-mono text-[var(--color-text-secondary)]">{capsuleId}</span> and destroy all data inside it. This cannot be undone.
			</p>

			{#if destroyError}
				<div class="mt-4 rounded-[var(--radius-input)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-3 py-2 text-meta text-[var(--color-red)]">
					{destroyError}
				</div>
			{/if}

			<div class="mt-6 flex justify-end gap-3">
				<button
					onclick={() => { showDestroy = false; }}
					disabled={destroying}
					class="rounded-[var(--radius-button)] border border-[var(--color-border)] px-4 py-2 text-ui text-[var(--color-text-secondary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)] disabled:opacity-50"
				>
					Cancel
				</button>
				<button
					onclick={handleDestroy}
					disabled={destroying}
					class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-red)] px-5 py-2 text-ui font-semibold text-white transition-all duration-150 hover:brightness-110 hover:-translate-y-px active:translate-y-0 disabled:opacity-50 disabled:hover:translate-y-0"
				>
					{#if destroying}
						<svg class="animate-spin" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
							<path d="M21 12a9 9 0 1 1-6.219-8.56" />
						</svg>
						Destroying...
					{:else}
						Destroy
					{/if}
				</button>
			</div>
		</div>
	</div>
{/if}
