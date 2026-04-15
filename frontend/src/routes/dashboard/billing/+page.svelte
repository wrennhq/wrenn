<script lang="ts">
	import Sidebar from '$lib/components/Sidebar.svelte';
	import { onMount } from 'svelte';
	import { auth } from '$lib/auth.svelte';

	let collapsed = $state(
		typeof window !== 'undefined'
			? localStorage.getItem('wrenn_sidebar_collapsed') === 'true'
			: false
	);

	type EndpointStatus = 'loading' | 'available' | 'not_available' | 'error';
	let status = $state<EndpointStatus>('loading');
	let errorMsg = $state<string | null>(null);

	async function probe() {
		status = 'loading';
		errorMsg = null;
		try {
			const headers: Record<string, string> = {};
			if (auth.token) headers['Authorization'] = `Bearer ${auth.token}`;

			const res = await fetch('/api/v1/billing', { headers });
			if (res.status === 404) {
				status = 'not_available';
			} else if (!res.ok) {
				status = 'error';
				try {
					const data = await res.json();
					errorMsg = data?.error?.message ?? `Server returned ${res.status}`;
				} catch {
					errorMsg = `Server returned ${res.status}`;
				}
			} else {
				status = 'available';
			}
		} catch {
			status = 'error';
			errorMsg = 'Unable to connect to the server';
		}
	}

	onMount(probe);
</script>

<svelte:head>
	<title>Wrenn — Billing</title>
</svelte:head>

<div class="flex h-screen overflow-hidden">
	<Sidebar bind:collapsed />

	<div class="flex flex-1 flex-col overflow-hidden">
		<main class="flex-1 overflow-y-auto bg-[var(--color-bg-0)]">
			<!-- Header -->
			<div class="px-7 pt-8">
				<h1 class="font-serif text-page text-[var(--color-text-bright)]">
					Billing
				</h1>
				<p class="mt-2 text-ui text-[var(--color-text-secondary)]">
					Subscription, invoices, and payment methods for your team.
				</p>
			</div>

			<div class="mt-6 border-b border-[var(--color-border)]"></div>

			<!-- Content -->
			<div class="p-8" style="animation: fadeUp 0.35s ease both">
				{#if status === 'loading'}
					<div class="flex items-center justify-center py-24">
						<div class="flex items-center gap-3 text-ui text-[var(--color-text-secondary)]">
							<svg class="animate-spin" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
								<path d="M21 12a9 9 0 1 1-6.219-8.56" />
							</svg>
							Loading billing data...
						</div>
					</div>
				{:else if status === 'error'}
					<div class="mb-4 flex items-center justify-between gap-4 rounded-[var(--radius-card)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-4 py-3 text-ui text-[var(--color-red)]">
						<span>{errorMsg}</span>
						<button
							onclick={probe}
							class="shrink-0 font-semibold underline-offset-2 hover:underline"
						>
							Try again
						</button>
					</div>
				{:else if status === 'not_available'}
					<div class="flex flex-col items-center justify-center py-[72px]">
						<!-- Icon with glow -->
						<div class="relative mb-5">
							<div class="absolute inset-0 -m-6 rounded-full" style="background: radial-gradient(circle, rgba(212,167,60,0.06) 0%, transparent 70%)"></div>
							<div class="relative flex h-14 w-14 items-center justify-center rounded-[var(--radius-card)] border border-[var(--color-amber)]/20 bg-[var(--color-bg-3)]" style="animation: iconFloat 4s ease-in-out infinite">
								<!-- Credit card icon -->
								<svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="var(--color-amber)" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
									<rect x="1" y="4" width="22" height="16" rx="3" />
									<path d="M1 10h22" />
								</svg>
							</div>
						</div>
						<p class="font-serif text-heading text-[var(--color-text-bright)]">
							Cloud Feature
						</p>
						<p class="mt-2 max-w-sm text-center text-ui leading-relaxed text-[var(--color-text-tertiary)]">
							Billing management is available on Wrenn Cloud.
						</p>

						<!-- Info badge -->
						<div class="mt-6 flex items-center gap-2.5 rounded-[var(--radius-card)] border border-[var(--color-border)] bg-[var(--color-bg-2)] px-4 py-3">
							<svg class="shrink-0" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="var(--color-text-muted)" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round">
								<circle cx="12" cy="12" r="10" />
								<line x1="12" y1="16" x2="12" y2="12" />
								<line x1="12" y1="8" x2="12.01" y2="8" />
							</svg>
							<span class="text-meta text-[var(--color-text-secondary)]">
								This instance is running in self-hosted mode
							</span>
						</div>
					</div>
				{:else}
					<!-- Available state — placeholder for when the endpoint exists -->
					<div class="text-ui text-[var(--color-text-secondary)]">
						Billing data will be displayed here.
					</div>
				{/if}
			</div>
		</main>

		<footer class="h-px shrink-0 bg-[var(--color-border)]"></footer>
	</div>
</div>
