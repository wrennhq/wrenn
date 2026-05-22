<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { page } from '$app/stores';
	import { goto } from '$app/navigation';
	import TerminalTab from '$lib/components/TerminalTab.svelte';
	import FilesTab from '$lib/components/FilesTab.svelte';
	import MetricsPanel from '$lib/components/MetricsPanel.svelte';
	import DestroyDialog from '$lib/components/DestroyDialog.svelte';
	import SnapshotDialog from '$lib/components/SnapshotDialog.svelte';
	import CopyButton from '$lib/components/CopyButton.svelte';
	import { toast } from '$lib/toast.svelte';
	import {
		getAdminCapsule,
		destroyAdminCapsule,
		snapshotAdminCapsule,
	} from '$lib/api/admin-capsules';
	import type { Capsule } from '$lib/api/capsules';
	import { subscribeAdminSSE } from '$lib/sse.svelte';
	import type { SSEEvent } from '$lib/api/events';

	const capsuleId: string = $page.params.id ?? '';
	const API_BASE = '/api/v1/admin/capsules';

	let capsule = $state<Capsule | null>(null);
	let capsuleLoading = $state(true);
	let capsuleError = $state<string | null>(null);

	// Destroy dialog
	let showDestroy = $state(false);

	// Snapshot dialog
	let showSnapshot = $state(false);

	const metricsAvailable = $derived(
		capsule?.status === 'running' || capsule?.status === 'paused'
	);

	const canSnapshot = $derived(
		capsule?.status === 'running' || capsule?.status === 'paused'
	);

	const canDestroy = $derived(
		capsule?.status === 'running' ||
		capsule?.status === 'paused' ||
		capsule?.status === 'hibernated'
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

	function statusColor(status: string): string {
		switch (status) {
			case 'running': return 'var(--color-accent)';
			case 'paused': case 'hibernated':  return 'var(--color-amber)';
			case 'error':   return 'var(--color-red)';
			case 'pending': case 'starting': case 'resuming': case 'pausing': case 'snapshotting': case 'stopping':
				return 'var(--color-blue)';
			default:        return 'var(--color-text-muted)';
		}
	}

	function statusBg(status: string): string {
		switch (status) {
			case 'running': return 'rgba(94,140,88,0.12)';
			case 'paused': case 'hibernated':  return 'rgba(212,167,60,0.12)';
			case 'error':   return 'rgba(207,129,114,0.12)';
			case 'pending': case 'starting': case 'resuming': case 'pausing': case 'snapshotting': case 'stopping':
				return 'rgba(90,159,212,0.12)';
			default:        return 'rgba(255,255,255,0.05)';
		}
	}

	function statusBorder(status: string): string {
		switch (status) {
			case 'running': return 'rgba(94,140,88,0.3)';
			case 'paused': case 'hibernated':  return 'rgba(212,167,60,0.3)';
			case 'error':   return 'rgba(207,129,114,0.3)';
			case 'pending': case 'starting': case 'resuming': case 'pausing': case 'snapshotting': case 'stopping':
				return 'rgba(90,159,212,0.3)';
			default:        return 'rgba(255,255,255,0.08)';
		}
	}

	let pollTimer: ReturnType<typeof setInterval> | null = null;
	let unsubscribe: (() => void) | null = null;

	function handleSSEEvent(event: SSEEvent) {
		if (!event.resource || event.resource.id !== capsuleId) return;
		if (event.event === 'capsule.destroy') {
			goto('/admin/capsules');
			return;
		}
		if (event.sandbox) {
			capsule = event.sandbox;
			return;
		}
		// Hydration failed server-side; pull fresh state so the badge doesn't
		// sit on a stale value until the next poll tick.
		void loadCapsule();
	}

	function startPolling() {
		stopPolling();
		pollTimer = setInterval(loadCapsule, 10_000);
	}

	function stopPolling() {
		if (pollTimer) { clearInterval(pollTimer); pollTimer = null; }
	}

	function handleVisibility() {
		if (document.hidden) {
			stopPolling();
		} else {
			loadCapsule();
			startPolling();
		}
	}

	onMount(() => {
		loadCapsule();
		startPolling();
		unsubscribe = subscribeAdminSSE(handleSSEEvent);
		document.addEventListener('visibilitychange', handleVisibility);
	});

	onDestroy(() => {
		stopPolling();
		unsubscribe?.();
		document.removeEventListener('visibilitychange', handleVisibility);
	});
</script>

<svelte:head>
	<title>Wrenn Admin — {capsuleId}</title>
</svelte:head>

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
			<!-- Breadcrumb + summary + actions -->
			<div class="shrink-0 px-7 pt-8">
				<div class="flex items-center gap-2.5">
					<a
						href="/admin/capsules"
						class="font-serif text-page leading-none text-[var(--color-text-secondary)] transition-colors duration-150 hover:text-[var(--color-text-bright)]"
					>
						Capsules
					</a>
					<span class="text-[var(--color-text-muted)] select-none" style="font-size: 1.1rem">&rsaquo;</span>
					<span class="flex items-center gap-1.5">
						<span class="font-mono text-[1.1rem] leading-none text-[var(--color-text-bright)]">{capsuleId}</span>
						<CopyButton value={capsuleId} />
					</span>

					<span
						class="ml-1 inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-label font-semibold uppercase tracking-[0.05em]"
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
					<span class="font-mono text-ui text-[var(--color-text-muted)]">{capsule.template} &middot; {capsule.vcpus}v &middot; {capsule.memory_mb}MB</span>

					<div class="ml-auto flex items-center gap-2">
						{#if canSnapshot}
							<button
								onclick={() => { showSnapshot = true; }}
								class="flex items-center gap-1.5 rounded-[var(--radius-button)] border border-[var(--color-accent)]/30 bg-[var(--color-accent)]/8 px-3 py-1.5 text-meta font-medium text-[var(--color-accent-bright)] transition-all duration-150 hover:bg-[var(--color-accent)]/15 hover:border-[var(--color-accent)]/50 disabled:opacity-50"
							>
								<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round"><path d="M14.5 4h-5L7 7H2v13a2 2 0 002 2h16a2 2 0 002-2V7h-5l-2.5-3z" /><circle cx="12" cy="15" r="3" /></svg>
								Snapshot
							</button>
						{/if}
						{#if canDestroy}
							<button
								onclick={() => { showDestroy = true; }}
								class="flex items-center gap-1.5 rounded-[var(--radius-button)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/8 px-3 py-1.5 text-meta font-medium text-[var(--color-red)] transition-all duration-150 hover:bg-[var(--color-red)]/15 hover:border-[var(--color-red)]/50"
							>
								<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="3 6 5 6 21 6" /><path d="M19 6v14a2 2 0 01-2 2H7a2 2 0 01-2-2V6m3 0V4a2 2 0 012-2h4a2 2 0 012 2v2" /></svg>
								Destroy
							</button>
						{/if}
					</div>
				</div>
			</div>

			<div class="mt-5 shrink-0 border-b border-[var(--color-border)]"></div>

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

		<!-- Status bar -->
		<footer
			class="flex h-7 shrink-0 items-center justify-end border-t border-[var(--color-border)] bg-[var(--color-bg-1)] px-7"
		>
			<div class="flex items-center gap-1.5">
				<span class="relative flex h-[5px] w-[5px]">
					<span class="animate-status-ping absolute inline-flex h-full w-full rounded-full bg-[var(--color-accent)]"></span>
					<span class="relative inline-flex h-[5px] w-[5px] rounded-full bg-[var(--color-accent)]"></span>
				</span>
				<span class="font-mono text-label uppercase tracking-[0.04em] text-[var(--color-text-secondary)]">All systems operational</span>
			</div>
		</footer>
</main>

{#snippet adminSnapshotDescription()}
	<p class="text-ui text-[var(--color-text-tertiary)]">The capsule moves to a <span class="font-mono text-[var(--color-blue)]">snapshotting</span> state while its memory and disk are written to a new platform template available to all teams, then returns to running. This runs in the background.</p>
{/snippet}

<SnapshotDialog
	open={showSnapshot}
	{capsuleId}
	onclose={() => { showSnapshot = false; }}
	onsnapshot={(updated) => { toast.success('Snapshot started'); capsule = updated; }}
	snapshotFn={snapshotAdminCapsule}
	title="Snapshot as platform template"
	label="Template name"
	placeholder="e.g. python-3.12, node-22-dev"
	hint="Leave blank for an auto-generated name. Each snapshot needs a unique name."
	confirmLabel="Snapshot"
	pendingLabel="Snapshotting..."
	description={adminSnapshotDescription}
/>

<DestroyDialog
	open={showDestroy}
	{capsuleId}
	onclose={() => { showDestroy = false; }}
	ondestroyed={() => { toast.success('Capsule destroyed'); goto('/admin/capsules'); }}
	destroyFn={destroyAdminCapsule}
/>
