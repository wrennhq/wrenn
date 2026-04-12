<script lang="ts">
	import AdminSidebar from '$lib/components/AdminSidebar.svelte';
	import CreateCapsuleDialog from '$lib/components/CreateCapsuleDialog.svelte';
	import CopyButton from '$lib/components/CopyButton.svelte';
	import { onMount, onDestroy } from 'svelte';
	import { goto } from '$app/navigation';
	import { toast } from '$lib/toast.svelte';
	import {
		listAdminCapsules,
		destroyAdminCapsule,
	} from '$lib/api/admin-capsules';
	import type { Capsule } from '$lib/api/capsules';

	let collapsed = $state(
		typeof window !== 'undefined'
			? localStorage.getItem('wrenn_sidebar_collapsed') === 'true'
			: false
	);

	let capsules = $state<Capsule[]>([]);
	let loading = $state(true);
	let error = $state<string | null>(null);
	let showCreateDialog = $state(false);

	// Destroy state
	let destroyTarget = $state<Capsule | null>(null);
	let destroying = $state(false);
	let destroyError = $state<string | null>(null);

	// Polling
	let pollInterval: ReturnType<typeof setInterval> | null = null;

	let runningCount = $derived(capsules.filter((c) => c.status === 'running').length);
	let pausedCount = $derived(capsules.filter((c) => c.status === 'paused').length);

	async function fetchCapsules() {
		const result = await listAdminCapsules();
		if (result.ok) {
			capsules = result.data;
			error = null;
		} else {
			error = result.error;
		}
		loading = false;
	}

	function handleCreated(capsule: Capsule) {
		goto(`/admin/capsules/${capsule.id}`);
	}

	async function handleDestroy() {
		if (!destroyTarget) return;
		destroying = true;
		destroyError = null;
		const result = await destroyAdminCapsule(destroyTarget.id);
		if (result.ok) {
			capsules = capsules.filter((c) => c.id !== destroyTarget!.id);
			destroyTarget = null;
			toast.success('Capsule destroyed');
		} else {
			destroyError = result.error;
		}
		destroying = false;
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

	function fmtDate(iso: string | null | undefined): string {
		if (!iso) return '—';
		return new Date(iso).toLocaleString([], {
			month: 'short', day: 'numeric',
			hour: '2-digit', minute: '2-digit',
		});
	}

	onMount(() => {
		fetchCapsules();
		pollInterval = setInterval(fetchCapsules, 15_000);
	});

	onDestroy(() => {
		if (pollInterval) clearInterval(pollInterval);
	});
</script>

<svelte:head>
	<title>Wrenn Admin — Capsules</title>
</svelte:head>

<div class="flex h-screen overflow-hidden bg-[var(--color-bg-0)]">
	<AdminSidebar bind:collapsed />

	<main class="flex min-w-0 flex-1 flex-col overflow-hidden">
		<!-- Header -->
		<header class="relative shrink-0 border-b border-[var(--color-border)] bg-[var(--color-bg-1)]">
			<div class="absolute inset-0 bg-gradient-to-b from-[var(--color-accent)]/[0.02] to-transparent pointer-events-none"></div>

			<div class="relative flex items-start justify-between px-8 pt-7 pb-5">
				<div>
					<h1 class="font-serif text-page leading-none tracking-[-0.03em] text-[var(--color-text-bright)]">
						Capsules
					</h1>
					<p class="mt-2 text-ui text-[var(--color-text-tertiary)]">
						Launch temporary capsules to build and snapshot platform templates.
					</p>
				</div>
				<button
					onclick={() => { showCreateDialog = true; }}
					class="group flex items-center gap-2.5 rounded-[var(--radius-button)] bg-[var(--color-accent)] px-5 py-2.5 text-ui font-semibold text-white shadow-sm transition-all duration-200 hover:shadow-[0_0_20px_var(--color-accent-glow-mid)] hover:brightness-115 hover:-translate-y-px active:translate-y-0"
				>
					<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" class="transition-transform duration-200 group-hover:rotate-90"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>
					Launch Capsule
				</button>
			</div>

			<!-- Stats strip -->
			{#if !loading && !error}
				<div class="relative flex items-center gap-3 px-8 pb-5">
					<span class="inline-flex items-center gap-1.5 rounded-full border border-[var(--color-border)] bg-[var(--color-bg-2)] px-2.5 py-1 text-label font-semibold text-[var(--color-text-secondary)]">
						<span class="font-mono text-[var(--color-text-bright)]">{capsules.length}</span>
						total
					</span>
					{#if runningCount > 0}
						<span class="inline-flex items-center gap-1.5 rounded-full border border-[var(--color-accent)]/25 bg-[var(--color-accent)]/8 px-2.5 py-1 text-label font-semibold text-[var(--color-accent-bright)]">
							<span class="h-[5px] w-[5px] rounded-full bg-[var(--color-accent)]" style="animation: wrenn-glow 2.5s ease-in-out infinite"></span>
							<span class="font-mono">{runningCount}</span>
							running
						</span>
					{/if}
					{#if pausedCount > 0}
						<span class="inline-flex items-center gap-1.5 rounded-full border border-[var(--color-amber)]/25 bg-[var(--color-amber)]/8 px-2.5 py-1 text-label font-semibold text-[var(--color-amber)]">
							<span class="font-mono">{pausedCount}</span>
							paused
						</span>
					{/if}
				</div>
			{/if}
		</header>

		<!-- Content -->
		<div class="flex-1 overflow-y-auto px-8 py-6">
			{#if loading}
				<div class="flex items-center justify-center py-24">
					<div class="flex items-center gap-3 text-ui text-[var(--color-text-secondary)]">
						<svg class="animate-spin" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 12a9 9 0 1 1-6.219-8.56" /></svg>
						Loading capsules...
					</div>
				</div>
			{:else if error}
				<div class="flex items-center gap-3 rounded-[var(--radius-card)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/8 px-5 py-4">
					<svg class="shrink-0 text-[var(--color-red)]" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
						<circle cx="12" cy="12" r="10" /><line x1="12" y1="8" x2="12" y2="12" /><line x1="12" y1="16" x2="12.01" y2="16" />
					</svg>
					<span class="text-ui text-[var(--color-red)]">{error}</span>
				</div>
			{:else if capsules.length === 0}
				<div class="flex flex-col items-center justify-center py-28 text-center">
					<div class="mb-5 flex h-16 w-16 items-center justify-center rounded-2xl border border-[var(--color-border)] bg-[var(--color-bg-2)]">
						<svg width="28" height="28" viewBox="0 0 24 24" fill="none" stroke="var(--color-text-muted)" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
							<polyline points="4 17 10 11 4 5" /><line x1="12" y1="19" x2="20" y2="19" />
						</svg>
					</div>
					<h2 class="font-serif text-heading tracking-[-0.02em] text-[var(--color-text-bright)]">No capsules</h2>
					<p class="mt-2 max-w-[340px] text-ui text-[var(--color-text-tertiary)]">
						Launch a capsule, configure it interactively, then snapshot it as a platform template.
					</p>
					<button
						onclick={() => { showCreateDialog = true; }}
						class="mt-6 flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-accent)] px-5 py-2.5 text-ui font-semibold text-white shadow-sm transition-all duration-200 hover:shadow-[0_0_20px_var(--color-accent-glow-mid)] hover:brightness-115 hover:-translate-y-px active:translate-y-0"
					>
						<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>
						Launch Capsule
					</button>
				</div>
			{:else}
				<!-- Capsule table -->
				<div class="overflow-hidden rounded-[var(--radius-card)] border border-[var(--color-border)]">
					<table class="w-full">
						<thead>
							<tr class="border-b border-[var(--color-border)] bg-[var(--color-bg-2)]">
								<th class="px-5 py-3 text-left text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">ID</th>
								<th class="px-5 py-3 text-left text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Status</th>
								<th class="px-5 py-3 text-left text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Template</th>
								<th class="px-5 py-3 text-left text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Specs</th>
								<th class="px-5 py-3 text-left text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Started</th>
								<th class="px-5 py-3 text-right text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Actions</th>
							</tr>
						</thead>
						<tbody class="divide-y divide-[var(--color-border)]">
							{#each capsules as capsule (capsule.id)}
								<tr class="group transition-colors duration-100 hover:bg-[var(--color-bg-2)]">
									<td class="px-5 py-3.5">
										<div class="flex items-center gap-2">
											<a
												href="/admin/capsules/{capsule.id}"
												class="font-mono text-ui text-[var(--color-text-bright)] transition-colors duration-150 hover:text-[var(--color-accent-bright)]"
											>
												{capsule.id}
											</a>
											<CopyButton value={capsule.id} />
										</div>
									</td>
									<td class="px-5 py-3.5">
										<span
											class="inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-label font-semibold uppercase tracking-[0.05em]"
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
									</td>
									<td class="px-5 py-3.5">
										<span class="font-mono text-ui text-[var(--color-text-secondary)]">{capsule.template}</span>
									</td>
									<td class="px-5 py-3.5">
										<span class="font-mono text-meta text-[var(--color-text-secondary)]">
											{capsule.vcpus}v &middot; {capsule.memory_mb}MB
										</span>
									</td>
									<td class="px-5 py-3.5">
										<span class="font-mono text-meta text-[var(--color-text-muted)]">{fmtDate(capsule.started_at)}</span>
									</td>
									<td class="px-5 py-3.5 text-right">
										<div class="flex items-center justify-end gap-2">
											<a
												href="/admin/capsules/{capsule.id}"
												class="rounded-[var(--radius-button)] border border-[var(--color-border)] px-3 py-1.5 text-meta font-medium text-[var(--color-text-secondary)] transition-colors duration-150 hover:bg-[var(--color-bg-3)] hover:text-[var(--color-text-primary)]"
											>
												Open
											</a>
											{#if capsule.status === 'running' || capsule.status === 'paused'}
												<button
													onclick={() => { destroyTarget = capsule; destroyError = null; }}
													class="rounded-[var(--radius-button)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/8 px-3 py-1.5 text-meta font-medium text-[var(--color-red)] transition-all duration-150 hover:bg-[var(--color-red)]/15 hover:border-[var(--color-red)]/50"
												>
													Destroy
												</button>
											{/if}
										</div>
									</td>
								</tr>
							{/each}
						</tbody>
					</table>
				</div>
			{/if}
		</div>
	</main>
</div>

<CreateCapsuleDialog
	open={showCreateDialog}
	onclose={() => { showCreateDialog = false; }}
	oncreated={handleCreated}
	templateSource="platform"
/>

<!-- Destroy confirmation dialog -->
{#if destroyTarget}
	<div class="fixed inset-0 z-50 flex items-center justify-center">
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="absolute inset-0 bg-black/60"
			onclick={() => { if (!destroying) { destroyTarget = null; } }}
			onkeydown={(e) => { if (e.key === 'Escape' && !destroying) { destroyTarget = null; } }}
		></div>
		<div class="relative w-full max-w-[380px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] p-6" style="animation: fadeUp 0.2s ease both; box-shadow: var(--shadow-dialog)">
			<h2 class="font-serif text-heading tracking-[-0.02em] text-[var(--color-text-bright)]">Destroy Capsule</h2>
			<p class="mt-2 text-ui text-[var(--color-text-tertiary)]">
				Terminate <span class="font-mono text-[var(--color-text-secondary)]">{destroyTarget.id}</span> and destroy all data inside it. This cannot be undone.
			</p>

			{#if destroyError}
				<div class="mt-4 rounded-[var(--radius-input)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-3 py-2 text-meta text-[var(--color-red)]">
					{destroyError}
				</div>
			{/if}

			<div class="mt-6 flex justify-end gap-3">
				<button
					onclick={() => { destroyTarget = null; }}
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
