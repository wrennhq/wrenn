<script lang="ts">
	import Sidebar from '$lib/components/Sidebar.svelte';
	import { capsuleRunningCount } from '$lib/capsule-store.svelte';
	import { page } from '$app/stores';

	let { children } = $props();

	let collapsed = $state(
		typeof window !== 'undefined'
			? localStorage.getItem('wrenn_sidebar_collapsed') === 'true'
			: false
	);

	let activeTab = $derived(
		$page.url.pathname.startsWith('/dashboard/capsules/stats') ? 'stats' : 'list'
	);
</script>

<svelte:head>
	<title>Wrenn — Capsules</title>
</svelte:head>

<div class="flex h-screen overflow-hidden">
	<Sidebar bind:collapsed />

	<div class="flex flex-1 flex-col overflow-hidden">
		<main class="flex-1 overflow-y-auto bg-[var(--color-bg-0)]">
			<!-- Header area -->
			<div class="px-7 pt-8">
				<!-- Top row: title + status chip -->
				<div class="flex items-center justify-between">
					<div>
						<h1 class="font-serif text-page tracking-[-0.02em] text-[var(--color-text-bright)]">
							Capsules
						</h1>
						<p class="mt-2 text-ui text-[var(--color-text-secondary)]">
							Isolated VMs. Start cold in under a second — pause, snapshot, or destroy at will.
						</p>
					</div>

					<div class="flex items-center gap-3">
						<!-- Status chip -->
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

				<!-- Tab bar -->
				<div class="mt-5 flex gap-1 border-b border-[var(--color-border)]">
					<a
						href="/dashboard/capsules/list"
						class="flex items-center gap-2 border-b-2 px-4 py-2.5 text-ui font-medium transition-colors duration-150 {activeTab === 'list'
							? 'border-[var(--color-accent)] text-[var(--color-accent-bright)]'
							: 'border-transparent text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]'}"
					>
						<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
							<line x1="8" y1="6" x2="21" y2="6" /><line x1="8" y1="12" x2="21" y2="12" /><line x1="8" y1="18" x2="21" y2="18" />
							<line x1="3" y1="6" x2="3.01" y2="6" /><line x1="3" y1="12" x2="3.01" y2="12" /><line x1="3" y1="18" x2="3.01" y2="18" />
						</svg>
						List
					</a>
					<a
						href="/dashboard/capsules/stats"
						class="flex items-center gap-2 border-b-2 px-4 py-2.5 text-ui font-medium transition-colors duration-150 {activeTab === 'stats'
							? 'border-[var(--color-accent)] text-[var(--color-accent-bright)]'
							: 'border-transparent text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]'}"
					>
						<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
							<polyline points="22 12 18 12 15 21 9 3 6 12 2 12" />
						</svg>
						Stats
					</a>
				</div>
			</div>

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
