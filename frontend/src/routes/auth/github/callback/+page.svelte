<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/stores';
	import { onMount } from 'svelte';
	import { auth } from '$lib/auth.svelte';
	import { teams } from '$lib/teams.svelte';
	import { updateName } from '$lib/api/me';
	import { IconUser, IconMail } from '$lib/components/icons';

	let showConfirmDialog = $state(false);
	let confirmName = $state('');
	let confirmEmail = $state('');
	let saving = $state(false);
	let nameError = $state('');

	function getCookie(name: string): string | null {
		const match = document.cookie.match(new RegExp(`(?:^|; )${name}=([^;]*)`));
		return match ? decodeURIComponent(match[1]) : null;
	}

	function clearSignupCookie() {
		document.cookie = `wrenn_oauth_new_signup=; path=/; max-age=0`;
	}

	async function finishLogin() {
		teams.reset();
		await auth.init();
		await goto('/dashboard');
	}

	async function handleConfirm() {
		saving = true;
		nameError = '';

		// Hydrate auth state first so the PATCH /v1/me request is authenticated.
		await auth.init();

		if (confirmName.trim() && confirmName.trim() !== auth.name) {
			const result = await updateName(confirmName.trim());
			if (!result.ok) {
				nameError = result.error;
				saving = false;
				return;
			}
			// Re-hydrate to pick up the new name.
			await auth.init();
		}

		teams.reset();
		await goto('/dashboard');
	}

	onMount(async () => {
		const params = $page.url.searchParams;
		const error = params.get('error');

		if (error) {
			goto(`/login?error=${encodeURIComponent(error)}`);
			return;
		}

		const isNewSignup = getCookie('wrenn_oauth_new_signup') === '1';
		clearSignupCookie();

		// Server has set the wrenn_sid cookie. Probe /v1/me to confirm and
		// populate identity state.
		await auth.init();

		if (!auth.isAuthenticated) {
			goto('/login?error=session_failed');
			return;
		}

		if (isNewSignup) {
			confirmName = auth.name ?? '';
			confirmEmail = auth.email ?? '';
			showConfirmDialog = true;
		} else {
			finishLogin();
		}
	});
</script>

{#if showConfirmDialog}
	<div class="flex min-h-screen items-center justify-center bg-[var(--color-bg-0)]">
		<div
			class="w-full max-w-[420px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)]"
			style="animation: fadeUp 0.2s ease both; box-shadow: var(--shadow-dialog)"
		>
			<div class="p-6">
				<h2 class="font-serif text-heading text-[var(--color-text-bright)]">Almost there</h2>
				<p class="mt-1.5 text-ui text-[var(--color-text-secondary)]">
					We pulled your details from GitHub. Change your display name if you'd like.
				</p>

				<div class="mt-5 space-y-3">
					<!-- Name (editable) -->
					<div>
						<label for="confirm-name" class="mb-1.5 block text-label uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]">
							Display name
						</label>
						<div class="group relative">
							<div class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-[var(--color-text-muted)] transition-colors duration-150 group-focus-within:text-[var(--color-accent)]">
								<IconUser size={14} />
							</div>
							<input
								id="confirm-name"
								type="text"
								bind:value={confirmName}
								class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-2)] py-3 pl-9 pr-3 text-body text-[var(--color-text-bright)] outline-none transition-all duration-150 placeholder:text-[var(--color-text-muted)] focus:border-[var(--color-accent)]"
							/>
						</div>
					</div>

					<!-- Email (read-only) -->
					<div>
						<label for="confirm-email" class="mb-1.5 block text-label uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]">
							Email
						</label>
						<div class="group relative">
							<div class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-[var(--color-text-muted)]">
								<IconMail size={14} />
							</div>
							<input
								id="confirm-email"
								type="email"
								value={confirmEmail}
								disabled
								class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-3)] py-3 pl-9 pr-3 text-body text-[var(--color-text-secondary)] outline-none cursor-not-allowed pointer-events-none"
							/>
						</div>
					</div>
				</div>

				{#if nameError}
					<p class="mt-3 text-ui text-[var(--color-red)]">{nameError}</p>
				{/if}

				<!-- Actions -->
				<div class="mt-6 flex justify-end">
					<button
						type="button"
						onclick={handleConfirm}
						disabled={saving || !confirmName.trim()}
						class="rounded-[var(--radius-button)] bg-[var(--color-accent)] px-5 py-2.5 text-body font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0 disabled:pointer-events-none disabled:opacity-50"
					>
						{#if saving}
							<span class="inline-flex items-center gap-2">
								<span class="inline-block h-3.5 w-3.5 animate-spin rounded-full border-2 border-white/30 border-t-white"></span>
								Setting up…
							</span>
						{:else}
							Get started
						{/if}
					</button>
				</div>
			</div>
		</div>
	</div>
{:else}
	<div class="flex min-h-screen items-center justify-center">
		<p class="text-ui text-[var(--color-text-secondary)]">Signing you in...</p>
	</div>
{/if}
