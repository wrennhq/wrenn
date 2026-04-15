<script lang="ts">
	import { requestPasswordReset } from '$lib/api/me';

	let email = $state('');
	let loading = $state(false);
	let submitted = $state(false);
	let error = $state('');

	async function handleSubmit(e: Event) {
		e.preventDefault();
		error = '';
		loading = true;
		await requestPasswordReset(email.trim().toLowerCase());
		// Always show success to avoid leaking account existence
		submitted = true;
		loading = false;
	}
</script>

<svelte:head>
	<title>Wrenn — Reset password</title>
</svelte:head>

<div class="flex min-h-screen items-center justify-center bg-[var(--color-bg-0)] px-4">
	<div class="w-full max-w-[400px]" style="animation: fadeUp 0.35s ease both">
		<!-- Brand -->
		<div class="mb-8 flex items-center gap-3">
			<img src="/logo.svg" alt="Wrenn" class="h-8 w-8 rounded-[var(--radius-logo)]" />
			<span class="font-brand text-[1.5rem] text-[var(--color-text-bright)]">Wrenn</span>
		</div>

		{#if submitted}
			<div class="rounded-[var(--radius-card)] border border-[var(--color-border)] bg-[var(--color-bg-1)] p-6">
				<h1 class="font-serif text-heading text-[var(--color-text-bright)]">Check your email</h1>
				<p class="mt-2 text-ui text-[var(--color-text-secondary)]">
					If an account exists for <span class="font-mono text-[var(--color-text-primary)]">{email}</span>, you'll receive a reset link shortly. The link expires in 15 minutes.
				</p>
				<a
					href="/login"
					class="mt-6 block text-center text-ui text-[var(--color-text-tertiary)] transition-colors duration-150 hover:text-[var(--color-text-secondary)]"
				>
					Back to sign in
				</a>
			</div>
		{:else}
			<div class="rounded-[var(--radius-card)] border border-[var(--color-border)] bg-[var(--color-bg-1)] p-6">
				<h1 class="font-serif text-heading text-[var(--color-text-bright)]">Reset your password</h1>
				<p class="mt-1 text-ui text-[var(--color-text-tertiary)]">
					Enter your email and we'll send you a reset link.
				</p>

				{#if error}
					<p class="mt-4 text-ui text-[var(--color-red)]">{error}</p>
				{/if}

				<form onsubmit={handleSubmit} class="mt-5 space-y-4">
					<div>
						<label
							for="email"
							class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]"
						>
							Email
						</label>
						<input
							id="email"
							type="email"
							bind:value={email}
							required
							disabled={loading}
							placeholder="you@example.com"
							autocomplete="email"
							class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 py-2 text-ui text-[var(--color-text-bright)] outline-none placeholder:text-[var(--color-text-muted)] transition-colors duration-150 focus:border-[var(--color-accent)] disabled:opacity-60"
						/>
					</div>

					<button
						type="submit"
						disabled={loading || !email.trim()}
						class="flex w-full items-center justify-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-accent)] py-2.5 text-ui font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0 disabled:opacity-50 disabled:hover:translate-y-0"
					>
						{#if loading}
							<svg class="animate-spin" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 12a9 9 0 1 1-6.219-8.56" /></svg>
							Sending…
						{:else}
							Send reset link
						{/if}
					</button>
				</form>

				<a
					href="/login"
					class="mt-5 block text-center text-meta text-[var(--color-text-tertiary)] transition-colors duration-150 hover:text-[var(--color-text-secondary)]"
				>
					Back to sign in
				</a>
			</div>
		{/if}
	</div>
</div>
