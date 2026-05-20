<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { getBuild, cancelBuild, type Build } from '$lib/api/builds';
	import { toast } from '$lib/toast.svelte';
	import BuildConsole from '$lib/components/BuildConsole.svelte';

	const buildId: string = $page.params.id ?? '';

	let build = $state<Build | null>(null);
	let loadError = $state<string | null>(null);
	let liveStatus = $state('');
	let canceling = $state(false);

	const status = $derived(liveStatus || build?.status || '');
	const canCancel = $derived(status === 'pending' || status === 'running');

	function statusColor(s: string): string {
		switch (s) {
			case 'success':
				return 'var(--color-accent-bright)';
			case 'failed':
				return 'var(--color-red)';
			case 'running':
				return 'var(--color-blue)';
			case 'cancelled':
				return 'var(--color-amber)';
			default:
				return 'var(--color-text-muted)';
		}
	}

	onMount(async () => {
		const r = await getBuild(buildId);
		if (r.ok) build = r.data;
		else loadError = r.error;
	});

	async function handleCancel() {
		canceling = true;
		const r = await cancelBuild(buildId);
		if (r.ok) toast.success('Build cancelled');
		else toast.error(r.error ?? 'Failed to cancel build');
		canceling = false;
	}
</script>

<div class="flex min-h-0 flex-1 flex-col">
	<header
		class="flex shrink-0 items-center gap-4 border-b border-[var(--color-border)] bg-[var(--color-bg-1)] px-6 py-4"
	>
		<a
			href="/admin/templates"
			class="flex items-center gap-1.5 text-ui text-[var(--color-text-tertiary)] transition-colors duration-150 hover:text-[var(--color-text-primary)]"
		>
			<svg
				width="15"
				height="15"
				viewBox="0 0 24 24"
				fill="none"
				stroke="currentColor"
				stroke-width="2"
				stroke-linecap="round"
				stroke-linejoin="round"
				aria-hidden="true"
			>
				<line x1="19" y1="12" x2="5" y2="12" />
				<polyline points="12 19 5 12 12 5" />
			</svg>
			Templates
		</a>

		<div class="h-4 w-px bg-[var(--color-border)]"></div>

		{#if build}
			<h1 class="font-serif text-heading leading-none text-[var(--color-text-bright)]">
				{build.name}
			</h1>
			<span class="font-mono text-meta text-[var(--color-text-muted)]">{buildId}</span>
		{:else}
			<span class="font-mono text-meta text-[var(--color-text-muted)]">{buildId}</span>
		{/if}

		<div class="flex-1"></div>

		{#if status}
			<span
				class="flex items-center gap-2 text-meta font-semibold capitalize"
				style="color: {statusColor(status)}"
			>
				{#if status === 'running' || status === 'pending'}
					<span class="relative flex h-2 w-2">
						<span
							class="animate-status-ping absolute inline-flex h-full w-full rounded-full opacity-60"
							style="background: {statusColor(status)}"
						></span>
						<span
							class="relative inline-flex h-2 w-2 rounded-full"
							style="background: {statusColor(status)}"
						></span>
					</span>
				{:else}
					<span class="h-2 w-2 rounded-full" style="background: {statusColor(status)}"></span>
				{/if}
				{status}
			</span>
		{/if}

		{#if canCancel}
			<button
				onclick={handleCancel}
				disabled={canceling}
				class="flex items-center gap-1.5 rounded-[var(--radius-button)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/8 px-3 py-1.5 text-meta font-medium text-[var(--color-red)] transition-colors duration-150 hover:bg-[var(--color-red)]/15 disabled:opacity-50"
			>
				{#if canceling}
					<svg
						class="animate-spin"
						width="11"
						height="11"
						viewBox="0 0 24 24"
						fill="none"
						stroke="currentColor"
						stroke-width="2"
						aria-hidden="true"
					>
						<path d="M21 12a9 9 0 1 1-6.219-8.56" />
					</svg>
				{:else}
					<svg
						width="11"
						height="11"
						viewBox="0 0 24 24"
						fill="none"
						stroke="currentColor"
						stroke-width="2.5"
						stroke-linecap="round"
						stroke-linejoin="round"
						aria-hidden="true"
					>
						<line x1="18" y1="6" x2="6" y2="18" />
						<line x1="6" y1="6" x2="18" y2="18" />
					</svg>
				{/if}
				Cancel build
			</button>
		{/if}
	</header>

	{#if loadError}
		<div class="flex flex-1 items-center justify-center">
			<div class="flex flex-col items-center gap-2 text-center">
				<span class="text-body font-medium text-[var(--color-text-secondary)]">
					Build not found
				</span>
				<span class="text-ui text-[var(--color-text-muted)]">{loadError}</span>
			</div>
		</div>
	{:else if build}
		<BuildConsole {buildId} {build} onStatusChange={(s) => (liveStatus = s)} />
	{:else}
		<div class="flex flex-1 items-center justify-center">
			<svg
				class="animate-spin text-[var(--color-text-tertiary)]"
				width="20"
				height="20"
				viewBox="0 0 24 24"
				fill="none"
				stroke="currentColor"
				stroke-width="2"
				aria-hidden="true"
			>
				<path d="M21 12a9 9 0 1 1-6.219-8.56" />
			</svg>
		</div>
	{/if}
</div>
