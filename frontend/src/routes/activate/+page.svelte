<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { goto } from '$app/navigation';
	import { auth } from '$lib/auth.svelte';
	import { teams } from '$lib/teams.svelte';
	import { apiActivate } from '$lib/api/auth';

	let loading = $state(true);
	let error = $state('');
	let done = $state(false);

	onMount(async () => {
		const token = $page.url.searchParams.get('token');
		if (!token) {
			error = 'No activation token provided.';
			loading = false;
			return;
		}

		const result = await apiActivate(token);
		loading = false;

		if (!result.ok) {
			error = result.error;
			return;
		}

		done = true;
		teams.reset();
		auth.login(result.data);
		goto('/dashboard');
	});
</script>

<svelte:head>
	<title>Wrenn — Activate account</title>
</svelte:head>

<div class="flex min-h-screen items-center justify-center bg-[var(--color-bg-0)] px-4">
	<div class="w-full max-w-[400px]" style="animation: fadeUp 0.35s ease both">
		<!-- Brand -->
		<div class="mb-8 flex items-center gap-3">
			<img src="/logo.svg" alt="Wrenn" class="h-10 w-10 rounded-[var(--radius-logo)]" />
			<span class="font-brand text-page text-[var(--color-text-bright)]">Wrenn</span>
		</div>

		{#if loading}
			<div class="rounded-[var(--radius-card)] border border-[var(--color-border)] bg-[var(--color-bg-1)] p-6">
				<div class="flex items-center gap-3">
					<span class="inline-block h-4 w-4 animate-spin rounded-full border-2 border-[var(--color-accent)]/30 border-t-[var(--color-accent)]"></span>
					<p class="text-body text-[var(--color-text-secondary)]">Activating your account...</p>
				</div>
			</div>
		{:else if error}
			<div class="rounded-[var(--radius-card)] border border-[var(--color-border)] bg-[var(--color-bg-1)] p-6">
				<h1 class="font-serif text-display text-[var(--color-text-bright)]">Activation failed</h1>
				<p class="mt-2 text-ui text-[var(--color-red)]">{error}</p>
				<a
					href="/login"
					class="mt-6 flex w-full items-center justify-center rounded-[var(--radius-button)] bg-[var(--color-accent)] py-3 text-body font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0"
				>
					Back to sign in
				</a>
			</div>
		{:else if done}
			<div class="rounded-[var(--radius-card)] border border-[var(--color-border)] bg-[var(--color-bg-1)] p-6">
				<div class="flex items-center gap-3">
					<span class="inline-block h-4 w-4 animate-spin rounded-full border-2 border-[var(--color-accent)]/30 border-t-[var(--color-accent)]"></span>
					<p class="text-body text-[var(--color-text-secondary)]">Redirecting to dashboard...</p>
				</div>
			</div>
		{/if}
	</div>
</div>
