<script lang="ts">
	import { page } from '$app/stores';
	import Sidebar from '$lib/components/Sidebar.svelte';
	import CopyButton from '$lib/components/CopyButton.svelte';
	import { capsuleRunningCount } from '$lib/capsule-store.svelte';

	let { children } = $props();

	let collapsed = $state(
		typeof window !== 'undefined'
			? localStorage.getItem('wrenn_sidebar_collapsed') === 'true'
			: false
	);
</script>

<svelte:head>
	<title>Wrenn — Capsules</title>
</svelte:head>

<div class="flex h-screen overflow-hidden">
	<Sidebar bind:collapsed />

	<div class="flex flex-1 flex-col overflow-hidden">
		<main class="flex flex-1 flex-col overflow-y-auto bg-[var(--color-bg-0)]">
			<!-- Header area -->
			{#if $page.params.id}
				<!-- Breadcrumb header for capsule detail (no border-b — tabs provide it) -->
				<div class="px-7 pt-8">
					<div class="flex items-center gap-2.5">
						<a
							href="/dashboard/capsules"
							class="font-serif text-page leading-none text-[var(--color-text-secondary)] transition-colors duration-150 hover:text-[var(--color-text-bright)]"
						>
							Capsules
						</a>
						<span class="text-[var(--color-text-muted)] select-none" style="font-size: 1.1rem">›</span>
						<span class="copy-host flex items-center gap-1.5">
							<span class="font-mono text-[1.1rem] leading-none text-[var(--color-text-bright)]">
								{$page.params.id}
							</span>
							<CopyButton value={$page.params.id} />
						</span>
					</div>
				</div>
			{:else}
				<!-- Default list header -->
				<div class="px-7 pt-8">
					<div class="flex items-center justify-between">
						<div>
							<h1 class="font-serif text-page text-[var(--color-text-bright)]">
								Capsules
							</h1>
							<p class="mt-2 text-ui text-[var(--color-text-secondary)]">
								All active and recent capsules across your team.
							</p>
						</div>

						<div class="flex items-center gap-3">
							<div
								class="flex items-center gap-2.5 rounded-[var(--radius-card)] border border-[var(--color-accent)]/20 bg-[var(--color-bg-2)] px-3.5 py-2"
							>
								<span class="relative flex h-[8px] w-[8px]">
									<span
										class="animate-status-ping absolute inline-flex h-full w-full rounded-full bg-[var(--color-accent)]"
									></span>
									<span class="relative inline-flex h-[8px] w-[8px] rounded-full bg-[var(--color-accent)]"></span>
								</span>
								<span class="font-mono text-body font-semibold text-[var(--color-accent-bright)]">{capsuleRunningCount.value}</span>
								<span class="text-ui text-[var(--color-text-secondary)]">running now</span>
							</div>
						</div>
					</div>

					<div class="mt-6 border-b border-[var(--color-border)]"></div>
				</div>
			{/if}

			{@render children()}
		</main>

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
	</div>
</div>
