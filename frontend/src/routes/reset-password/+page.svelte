<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { goto } from '$app/navigation';
	import { confirmPasswordReset } from '$lib/api/me';
	import { IconLock } from '$lib/components/icons';

	let token = $state('');
	let newPassword = $state('');
	let confirmPassword = $state('');
	let loading = $state(false);
	let error = $state('');
	let done = $state(false);

	onMount(() => {
		token = $page.url.searchParams.get('token') ?? '';
		if (!token) {
			goto('/forgot-password');
		}
	});

	async function handleSubmit(e: Event) {
		e.preventDefault();
		error = '';

		if (newPassword !== confirmPassword) {
			error = 'Passwords do not match.';
			return;
		}
		if (newPassword.length < 8) {
			error = 'Password must be at least 8 characters.';
			return;
		}

		loading = true;
		const result = await confirmPasswordReset(token, newPassword);
		if (result.ok) {
			done = true;
		} else {
			error = result.error;
		}
		loading = false;
	}
</script>

<svelte:head>
	<title>Wrenn — Set new password</title>
</svelte:head>

<div class="flex min-h-screen items-center justify-center bg-[var(--color-bg-0)] px-4">
	<div class="w-full max-w-[400px]" style="animation: fadeUp 0.35s ease both">
		<!-- Brand -->
		<div class="mb-8 flex items-center gap-3">
			<img src="/logo.svg" alt="Wrenn" class="h-10 w-10 rounded-[var(--radius-logo)]" />
			<span class="font-brand text-page text-[var(--color-text-bright)]">Wrenn</span>
		</div>

		{#if done}
			<div class="rounded-[var(--radius-card)] border border-[var(--color-border)] bg-[var(--color-bg-1)] p-6" style="animation: fadeUp 0.3s ease both">
				<h1 class="font-serif text-display text-[var(--color-text-bright)]">All set</h1>
				<p class="mt-1 text-ui text-[var(--color-text-secondary)]">
					Your password has been updated. Sign in to continue.
				</p>
				<a
					href="/login"
					class="mt-6 flex w-full items-center justify-center rounded-[var(--radius-button)] bg-[var(--color-accent)] py-3 text-body font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0"
				>
					Sign in
				</a>
			</div>
		{:else}
			<div class="rounded-[var(--radius-card)] border border-[var(--color-border)] bg-[var(--color-bg-1)] p-6">
				<h1 class="font-serif text-display text-[var(--color-text-bright)]">Set new password</h1>
				<p class="mt-1 text-ui text-[var(--color-text-tertiary)]">Must be at least 8 characters.</p>

				{#if error}
					<p class="mt-4 text-ui text-[var(--color-red)]">{error}</p>
				{/if}

				<form onsubmit={handleSubmit} class="mt-6 space-y-3">
					<div class="group relative">
						<div class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-[var(--color-text-muted)] transition-colors duration-150 group-focus-within:text-[var(--color-accent)]">
							<IconLock size={14} />
						</div>
						<input
							id="new-password"
							type="password"
							bind:value={newPassword}
							required
							disabled={loading}
							placeholder="New password"
							autocomplete="new-password"
							class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-2)] py-3 pl-9 pr-3 text-body text-[var(--color-text-bright)] outline-none placeholder:text-[var(--color-text-muted)] transition-all duration-150 focus:border-[var(--color-accent)] disabled:opacity-60"
						/>
					</div>

					<div class="group relative">
						<div class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-[var(--color-text-muted)] transition-colors duration-150 group-focus-within:text-[var(--color-accent)]">
							<IconLock size={14} />
						</div>
						<input
							id="confirm-password"
							type="password"
							bind:value={confirmPassword}
							required
							disabled={loading}
							placeholder="Confirm password"
							autocomplete="new-password"
							class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-2)] py-3 pl-9 pr-3 text-body text-[var(--color-text-bright)] outline-none placeholder:text-[var(--color-text-muted)] transition-all duration-150 focus:border-[var(--color-accent)] disabled:opacity-60"
						/>
					</div>

					<button
						type="submit"
						disabled={loading || !newPassword || !confirmPassword}
						class="!mt-5 w-full rounded-[var(--radius-button)] bg-[var(--color-accent)] py-3 text-body font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0 disabled:opacity-50 disabled:hover:translate-y-0"
					>
						{#if loading}
							<span class="inline-flex items-center gap-2">
								<span class="inline-block h-3.5 w-3.5 animate-spin rounded-full border-2 border-white/30 border-t-white"></span>
								Updating…
							</span>
						{:else}
							Set password
						{/if}
					</button>
				</form>
			</div>
		{/if}

		<a
			href="/login"
			class="mt-5 block text-center text-meta text-[var(--color-text-tertiary)] transition-colors duration-150 hover:text-[var(--color-text-secondary)]"
		>
			Back to sign in
		</a>
	</div>
</div>
