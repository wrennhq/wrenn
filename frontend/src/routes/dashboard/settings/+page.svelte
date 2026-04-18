<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { page } from '$app/stores';
	import { goto } from '$app/navigation';
	import { auth } from '$lib/auth.svelte';
	import { toast } from '$lib/toast.svelte';
	import {
		getMe,
		updateName,
		changePassword,
		requestPasswordReset,
		getProviderConnectURL,
		disconnectProvider,
		deleteAccount,
		type MeResponse
	} from '$lib/api/me';

	let me = $state<MeResponse | null>(null);
	let loadError = $state<string | null>(null);

	let initials = $derived(
		me?.name
			? me.name.split(' ').map(w => w[0]).join('').toUpperCase().slice(0, 2)
			: me?.email?.[0]?.toUpperCase() ?? '?'
	);

	// Profile
	let editName = $state('');
	let savingName = $state(false);
	let nameError = $state<string | null>(null);
	let nameSaved = $state(false);
	let nameSavedTimer: ReturnType<typeof setTimeout> | null = null;

	// Password
	let currentPassword = $state('');
	let newPassword = $state('');
	let confirmPassword = $state('');
	let savingPassword = $state(false);
	let passwordError = $state<string | null>(null);
	let sendingReset = $state(false);
	let passwordSaved = $state(false);
	let passwordSavedTimer: ReturnType<typeof setTimeout> | null = null;

	// GitHub connect/disconnect
	let connectingGitHub = $state(false);
	let disconnectingGitHub = $state(false);
	let showDisconnectConfirm = $state(false);
	let disconnectError = $state<string | null>(null);

	// Delete account
	let showDeleteConfirm = $state(false);
	let deleteConfirmation = $state('');
	let deleting = $state(false);
	let deleteError = $state<string | null>(null);

	const connectErrors: Record<string, string> = {
		already_linked: 'This GitHub account is already connected to another Wrenn account.',
		db_error: 'Something went wrong — please try again.',
		invalid_state: 'The connection attempt expired — please try again.',
		access_denied: 'GitHub access was denied.',
		exchange_failed: 'Authentication failed — please try again.'
	};

	async function fetchMe() {
		const result = await getMe();
		if (result.ok) {
			me = result.data;
			editName = result.data.name;
		} else {
			loadError = result.error;
		}
	}

	async function handleSaveName() {
		if (!editName.trim() || editName.trim() === me?.name) return;
		savingName = true;
		nameError = null;
		const result = await updateName(editName.trim());
		if (result.ok) {
			auth.login(result.data);
			me = { ...me!, name: result.data.name };
			editName = result.data.name;
			toast.success('Name updated.');
			nameSaved = true;
			if (nameSavedTimer) clearTimeout(nameSavedTimer);
			nameSavedTimer = setTimeout(() => (nameSaved = false), 1500);
		} else {
			nameError = result.error;
		}
		savingName = false;
	}

	async function handleSendPasswordReset() {
		if (!me) return;
		sendingReset = true;
		const result = await requestPasswordReset(me.email);
		sendingReset = false;
		if (result.ok) {
			toast.success('Password reset link sent to your email.');
		} else {
			toast.error(result.error);
		}
	}

	async function handleChangePassword() {
		savingPassword = true;
		passwordError = null;

		const body = me?.has_password
			? { current_password: currentPassword, new_password: newPassword }
			: { new_password: newPassword, confirm_password: confirmPassword };

		const result = await changePassword(body);
		if (result.ok) {
			currentPassword = '';
			newPassword = '';
			confirmPassword = '';
			const wasPasswordSet = !!me?.has_password;
			if (me) me = { ...me, has_password: true };
			toast.success(wasPasswordSet ? 'Password updated.' : 'Password added.');
			passwordSaved = true;
			if (passwordSavedTimer) clearTimeout(passwordSavedTimer);
			passwordSavedTimer = setTimeout(() => (passwordSaved = false), 1500);
		} else {
			passwordError = result.error;
		}
		savingPassword = false;
	}

	async function handleConnectGitHub() {
		connectingGitHub = true;
		const result = await getProviderConnectURL('github');
		if (result.ok) {
			window.location.href = result.data.auth_url;
		} else {
			toast.error(result.error);
			connectingGitHub = false;
		}
	}

	async function handleDisconnectGitHub() {
		disconnectingGitHub = true;
		disconnectError = null;
		const result = await disconnectProvider('github');
		if (result.ok) {
			me = { ...me!, providers: me!.providers.filter((p) => p !== 'github') };
			showDisconnectConfirm = false;
			toast.success('GitHub disconnected.');
		} else {
			disconnectError = result.error;
		}
		disconnectingGitHub = false;
	}

	async function handleDeleteAccount() {
		deleting = true;
		deleteError = null;
		const result = await deleteAccount(deleteConfirmation);
		if (result.ok) {
			auth.logout();
		} else {
			deleteError = result.error;
			deleting = false;
		}
	}

	onMount(async () => {
		await fetchMe();

		// Read OAuth callback params and clean URL immediately,
		// regardless of whether fetchMe succeeds.
		const connected = $page.url.searchParams.get('connected');
		const connectErr = $page.url.searchParams.get('connect_error');
		if (connected || connectErr) {
			goto('/dashboard/settings', { replaceState: true });
		}

		if (connected) {
			if (me) me = { ...me, providers: [...new Set([...me.providers, connected])] };
			toast.success(`${connected.charAt(0).toUpperCase() + connected.slice(1)} connected successfully.`);
		} else if (connectErr) {
			toast.error(connectErrors[connectErr] ?? 'Failed to connect account.');
		}
	});

	onDestroy(() => {
		if (nameSavedTimer) clearTimeout(nameSavedTimer);
		if (passwordSavedTimer) clearTimeout(passwordSavedTimer);
	});
</script>

<svelte:head>
	<title>Wrenn — Settings</title>
</svelte:head>

<main class="flex-1 overflow-y-auto bg-[var(--color-bg-0)]">
			<!-- Header -->
			<div class="px-7 pt-8">
				<h1 class="font-serif text-page text-[var(--color-text-bright)]">Settings</h1>
				<p class="mt-2 text-ui text-[var(--color-text-secondary)]">
					Manage your account details and security.
				</p>
				<div class="mt-6 border-b border-[var(--color-border)]"></div>
			</div>

			<!-- Content -->
			<div class="p-8">
				{#if loadError}
					<div class="rounded-[var(--radius-card)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-4 py-3 text-ui text-[var(--color-red)]" style="animation: fadeUp 0.35s ease both">
						{loadError}
					</div>
				{:else if me}
					<div class="mx-auto max-w-[560px] space-y-8">

						<!-- ── Profile ── -->
						<section style="animation: fadeUp 0.35s ease both">
							<div class="flex items-center gap-4">
								<div class="avatar-ring flex h-14 w-14 shrink-0 items-center justify-center rounded-full border border-[var(--color-border-mid)] bg-[var(--color-bg-3)]">
									<span class="font-serif text-heading leading-none text-[var(--color-text-bright)]">{initials}</span>
								</div>
								<div>
									<h2 class="font-serif text-heading text-[var(--color-text-bright)]">Profile</h2>
									<p class="mt-0.5 text-ui text-[var(--color-text-tertiary)]">How you appear across Wrenn.</p>
								</div>
							</div>

							<div class="mt-6 space-y-4">
								<div>
									<label
										for="display-name"
										class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]"
									>
										Display name
									</label>
									<input
										id="display-name"
										type="text"
										bind:value={editName}
										disabled={savingName}
										placeholder="Your name"
										class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 py-2 text-ui text-[var(--color-text-bright)] outline-none placeholder:text-[var(--color-text-muted)] transition-[color,border-color,box-shadow] duration-150 focus:border-[var(--color-accent)] focus:shadow-[0_0_0_2px_var(--color-accent-glow)] disabled:opacity-60"
									/>
								</div>

								<div>
									<span class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]">
										Email
									</span>
									<div class="rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-1)] px-3 py-2 font-mono text-ui text-[var(--color-text-secondary)]">
										{me.email}
									</div>
								</div>

								{#if nameError}
									<p class="text-ui text-[var(--color-red)]">{nameError}</p>
								{/if}

								<div class="flex justify-end">
									<button
										onclick={handleSaveName}
										disabled={savingName || nameSaved || !editName.trim() || editName.trim() === me.name}
										class="flex items-center gap-2 rounded-[var(--radius-button)] px-4 py-2 text-ui font-semibold text-white transition-all duration-150 hover:-translate-y-px active:translate-y-0 disabled:hover:translate-y-0 {nameSaved ? 'bg-[var(--color-accent-bright)]' : 'bg-[var(--color-accent)] hover:brightness-115 disabled:opacity-50'}"
									>
										{#if savingName}
											<svg class="animate-spin" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 12a9 9 0 1 1-6.219-8.56" /></svg>
											Saving…
										{:else if nameSaved}
											<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round" class="check-draw"><polyline points="20 6 9 17 4 12" /></svg>
											Saved
										{:else}
											Save
										{/if}
									</button>
								</div>
							</div>
						</section>

						<div class="border-t border-[var(--color-border)]"></div>

						<!-- ── Security ── -->
						<section style="animation: fadeUp 0.35s ease both; animation-delay: 60ms">
							<div class="flex items-start gap-3">
								<div class="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-[var(--radius-avatar)] border border-[var(--color-border)] bg-[var(--color-bg-2)]">
									<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="text-[var(--color-text-tertiary)]"><rect x="3" y="11" width="18" height="11" rx="2" ry="2" /><path d="M7 11V7a5 5 0 0 1 10 0v4" /></svg>
								</div>
								<div>
									<h2 class="font-serif text-heading text-[var(--color-text-bright)]">
										{me.has_password ? 'Change password' : 'Add a password'}
									</h2>
									<p class="mt-0.5 text-ui text-[var(--color-text-tertiary)]">
										{me.has_password
											? 'Use a strong, unique password you don\'t use elsewhere.'
											: 'Set a password so you can sign in with your email.'}
									</p>
								</div>
							</div>

							<div class="mt-5 space-y-4">
								{#if me.has_password}
									<div>
										<label
											for="current-password"
											class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]"
										>
											Current password
										</label>
										<input
											id="current-password"
											type="password"
											bind:value={currentPassword}
											disabled={savingPassword}
											autocomplete="current-password"
											class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 py-2 text-ui text-[var(--color-text-bright)] outline-none placeholder:text-[var(--color-text-muted)] transition-[color,border-color,box-shadow] duration-150 focus:border-[var(--color-accent)] focus:shadow-[0_0_0_2px_var(--color-accent-glow)] disabled:opacity-60"
										/>
									</div>
								{/if}

								<div>
									<label
										for="new-password"
										class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]"
									>
										New password
									</label>
									<input
										id="new-password"
										type="password"
										bind:value={newPassword}
										disabled={savingPassword}
										autocomplete="new-password"
										placeholder="Min. 8 characters"
										class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 py-2 text-ui text-[var(--color-text-bright)] outline-none placeholder:text-[var(--color-text-muted)] transition-[color,border-color,box-shadow] duration-150 focus:border-[var(--color-accent)] focus:shadow-[0_0_0_2px_var(--color-accent-glow)] disabled:opacity-60"
									/>
								</div>

								{#if !me.has_password}
									<div>
										<label
											for="confirm-password"
											class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]"
										>
											Confirm password
										</label>
										<input
											id="confirm-password"
											type="password"
											bind:value={confirmPassword}
											disabled={savingPassword}
											autocomplete="new-password"
											class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 py-2 text-ui text-[var(--color-text-bright)] outline-none placeholder:text-[var(--color-text-muted)] transition-[color,border-color,box-shadow] duration-150 focus:border-[var(--color-accent)] focus:shadow-[0_0_0_2px_var(--color-accent-glow)] disabled:opacity-60"
										/>
									</div>
								{/if}

								{#if passwordError}
									<p class="text-ui text-[var(--color-red)]">{passwordError}</p>
								{/if}

								<div class="flex items-center justify-between">
									{#if me.has_password}
										<button
											type="button"
											onclick={handleSendPasswordReset}
											disabled={sendingReset}
											class="text-meta text-[var(--color-text-tertiary)] transition-colors duration-150 hover:text-[var(--color-text-secondary)] disabled:opacity-50"
										>
											{sendingReset ? 'Sending…' : 'Forgot password?'}
										</button>
									{:else}
										<span></span>
									{/if}

									<button
										onclick={handleChangePassword}
										disabled={savingPassword || passwordSaved || !newPassword || (me.has_password && !currentPassword) || (!me.has_password && !confirmPassword)}
										class="flex items-center gap-2 rounded-[var(--radius-button)] px-4 py-2 text-ui font-semibold text-white transition-all duration-150 hover:-translate-y-px active:translate-y-0 disabled:hover:translate-y-0 {passwordSaved ? 'bg-[var(--color-accent-bright)]' : 'bg-[var(--color-accent)] hover:brightness-115 disabled:opacity-50'}"
									>
										{#if savingPassword}
											<svg class="animate-spin" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 12a9 9 0 1 1-6.219-8.56" /></svg>
											Saving…
										{:else if passwordSaved}
											<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round" class="check-draw"><polyline points="20 6 9 17 4 12" /></svg>
											Saved
										{:else}
											{me.has_password ? 'Update password' : 'Set password'}
										{/if}
									</button>
								</div>
							</div>
						</section>

						<div class="border-t border-[var(--color-border)]"></div>

						<!-- ── Connected Accounts ── -->
						<section style="animation: fadeUp 0.35s ease both; animation-delay: 120ms">
							<div class="flex items-start gap-3">
								<div class="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-[var(--radius-avatar)] border border-[var(--color-border)] bg-[var(--color-bg-2)]">
									<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="text-[var(--color-text-tertiary)]"><path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71" /><path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71" /></svg>
								</div>
								<div>
									<h2 class="font-serif text-heading text-[var(--color-text-bright)]">Connected accounts</h2>
									<p class="mt-0.5 text-ui text-[var(--color-text-tertiary)]">
										Sign in with a linked account instead of your password.
									</p>
								</div>
							</div>

							<div class="mt-5">
								<!-- GitHub row -->
								<div class="flex items-center justify-between rounded-[var(--radius-card)] border px-4 py-3 transition-colors duration-200 {me.providers.includes('github') ? 'border-[var(--color-accent)]/30 bg-[var(--color-accent-glow)]' : 'border-[var(--color-border)] bg-[var(--color-bg-1)]'}">
									<div class="flex items-center gap-3">
										<!-- GitHub icon -->
										<svg width="20" height="20" viewBox="0 0 24 24" fill="currentColor" class="{me.providers.includes('github') ? 'text-[var(--color-text-bright)]' : 'text-[var(--color-text-secondary)]'}">
											<path d="M12 0C5.37 0 0 5.37 0 12c0 5.31 3.435 9.795 8.205 11.385.6.105.825-.255.825-.57 0-.285-.015-1.23-.015-2.235-3.015.555-3.795-.735-4.035-1.41-.135-.345-.72-1.41-1.23-1.695-.42-.225-1.02-.78-.015-.795.945-.015 1.62.87 1.845 1.23 1.08 1.815 2.805 1.305 3.495.99.105-.78.42-1.305.765-1.605-2.67-.3-5.46-1.335-5.46-5.925 0-1.305.465-2.385 1.23-3.225-.12-.3-.54-1.53.12-3.18 0 0 1.005-.315 3.3 1.23.96-.27 1.98-.405 3-.405s2.04.135 3 .405c2.295-1.56 3.3-1.23 3.3-1.23.66 1.65.24 2.88.12 3.18.765.84 1.23 1.905 1.23 3.225 0 4.605-2.805 5.625-5.475 5.925.435.375.81 1.095.81 2.22 0 1.605-.015 2.895-.015 3.3 0 .315.225.69.825.57A12.02 12.02 0 0 0 24 12c0-6.63-5.37-12-12-12z" />
										</svg>
										<div>
											<div class="text-ui font-medium text-[var(--color-text-primary)]">GitHub</div>
											{#if me.providers.includes('github')}
												<div class="flex items-center gap-1 text-meta text-[var(--color-accent)]">
													<svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round" class="check-draw"><polyline points="20 6 9 17 4 12" /></svg>
													Connected
												</div>
											{:else}
												<div class="text-meta text-[var(--color-text-muted)]">Not connected</div>
											{/if}
										</div>
									</div>

									{#if me.providers.includes('github')}
										<button
											onclick={() => { showDisconnectConfirm = true; disconnectError = null; }}
											class="rounded-[var(--radius-button)] border border-[var(--color-border)] px-3 py-1.5 text-ui text-[var(--color-text-secondary)] transition-colors duration-150 hover:border-[var(--color-red)]/50 hover:text-[var(--color-red)]"
										>
											Disconnect
										</button>
									{:else}
										<button
											onclick={handleConnectGitHub}
											disabled={connectingGitHub}
											class="flex items-center gap-2 rounded-[var(--radius-button)] border border-[var(--color-border)] px-3 py-1.5 text-ui text-[var(--color-text-secondary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)] disabled:opacity-50"
										>
											{#if connectingGitHub}
												<svg class="animate-spin" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 12a9 9 0 1 1-6.219-8.56" /></svg>
											{/if}
											Connect
										</button>
									{/if}
								</div>
							</div>
						</section>

						<div class="border-t border-[var(--color-border)]"></div>

						<!-- ── Danger Zone ── -->
						<section style="animation: fadeUp 0.35s ease both; animation-delay: 180ms">
							<h2 class="font-serif text-heading text-[var(--color-red)]">Danger zone</h2>
							<p class="mt-1 text-ui text-[var(--color-text-tertiary)]">
								Deleting your account is irreversible.
							</p>

							<div class="mt-5 rounded-[var(--radius-card)] border border-[var(--color-red)]/25 border-l-[2px] border-l-[var(--color-red)]/40 bg-[var(--color-red)]/[0.03] px-4 py-4">
								<div class="flex items-start justify-between gap-4">
									<div>
										<div class="text-ui font-medium text-[var(--color-text-primary)]">Delete account</div>
										<div class="mt-0.5 text-meta text-[var(--color-text-muted)]">
											Your account will be deactivated immediately and permanently removed after 15 days.
										</div>
									</div>
									<button
										onclick={() => { showDeleteConfirm = true; deleteConfirmation = ''; deleteError = null; }}
										class="shrink-0 rounded-[var(--radius-button)] border border-[var(--color-red)]/30 px-3 py-1.5 text-ui text-[var(--color-red)] transition-colors duration-150 hover:bg-[var(--color-red)]/10"
									>
										Delete account
									</button>
								</div>
							</div>
						</section>

					</div>
				{:else}
					<!-- Loading skeleton -->
					<div class="mx-auto max-w-[560px] space-y-6">
						<div class="flex items-center gap-4" style="animation: fadeUp 0.35s ease both">
							<div class="h-14 w-14 shrink-0 animate-pulse rounded-full bg-[var(--color-bg-3)]"></div>
							<div class="flex-1 space-y-2">
								<div class="h-4 w-24 animate-pulse rounded bg-[var(--color-bg-3)]"></div>
								<div class="h-3 w-40 animate-pulse rounded bg-[var(--color-bg-2)]"></div>
							</div>
						</div>
						{#each [140, 180, 100] as h, i}
							<div style="animation: fadeUp 0.35s ease both; animation-delay: {(i + 1) * 60}ms">
								<div class="animate-pulse rounded-[var(--radius-card)] bg-[var(--color-bg-2)]" style="height: {h}px"></div>
							</div>
						{/each}
					</div>
				{/if}
			</div>
		</main>
<footer class="flex h-7 shrink-0 items-center justify-end border-t border-[var(--color-border)] bg-[var(--color-bg-1)] px-7">
	<div class="flex items-center gap-1.5">
		<span class="relative flex h-[5px] w-[5px]">
			<span class="animate-status-ping absolute inline-flex h-full w-full rounded-full bg-[var(--color-accent)]"></span>
			<span class="relative inline-flex h-[5px] w-[5px] rounded-full bg-[var(--color-accent)]"></span>
		</span>
		<span class="font-mono text-label uppercase tracking-[0.04em] text-[var(--color-text-secondary)]">All systems operational</span>
	</div>
</footer>

<!-- Disconnect GitHub dialog -->
{#if showDisconnectConfirm}
	<div class="fixed inset-0 z-50 flex items-center justify-center">
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="absolute inset-0 bg-black/60 backdrop-fade"
			onclick={() => { if (!disconnectingGitHub) showDisconnectConfirm = false; }}
			onkeydown={(e) => { if (e.key === 'Escape' && !disconnectingGitHub) showDisconnectConfirm = false; }}
		></div>
		<div
			class="relative w-full max-w-[420px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] p-6"
			style="animation: fadeUp 0.2s ease both; box-shadow: var(--shadow-dialog)"
		>
			<h2 class="font-serif text-heading text-[var(--color-text-bright)]">Disconnect GitHub</h2>
			<p class="mt-1 text-ui text-[var(--color-text-tertiary)]">
				You won't be able to sign in with GitHub. You can reconnect it later.
			</p>

			{#if disconnectError}
				<div class="mt-4 rounded-[var(--radius-input)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-3 py-2 text-meta text-[var(--color-red)]">
					{disconnectError}
				</div>
			{/if}

			<div class="mt-6 flex justify-end gap-3">
				<button
					onclick={() => showDisconnectConfirm = false}
					disabled={disconnectingGitHub}
					class="rounded-[var(--radius-button)] border border-[var(--color-border)] px-4 py-2 text-ui text-[var(--color-text-secondary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)] disabled:opacity-50"
				>
					Cancel
				</button>
				<button
					onclick={handleDisconnectGitHub}
					disabled={disconnectingGitHub}
					class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-red)] px-5 py-2 text-ui font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0 disabled:opacity-50 disabled:hover:translate-y-0"
				>
					{#if disconnectingGitHub}
						<svg class="animate-spin" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 12a9 9 0 1 1-6.219-8.56" /></svg>
					{/if}
					Disconnect
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Delete account dialog -->
{#if showDeleteConfirm}
	<div class="fixed inset-0 z-50 flex items-center justify-center">
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="absolute inset-0 bg-black/60 backdrop-fade"
			onclick={() => { if (!deleting) showDeleteConfirm = false; }}
			onkeydown={(e) => { if (e.key === 'Escape' && !deleting) showDeleteConfirm = false; }}
		></div>
		<div
			class="relative w-full max-w-[420px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] p-6"
			style="animation: fadeUp 0.2s ease both; box-shadow: var(--shadow-dialog)"
		>
			<h2 class="font-serif text-heading text-[var(--color-text-bright)]">Delete account</h2>
			<p class="mt-1 text-ui text-[var(--color-text-tertiary)]">
				Your account will be deactivated immediately and permanently deleted after 15 days. This cannot be undone.
			</p>

			{#if deleteError}
				<div class="mt-4 rounded-[var(--radius-input)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-3 py-2 text-meta text-[var(--color-red)]">
					{deleteError}
				</div>
			{/if}

			<div class="mt-5">
				<label
					for="delete-confirm"
					class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]"
				>
					Type your email to confirm
				</label>
				<input
					id="delete-confirm"
					type="email"
					bind:value={deleteConfirmation}
					disabled={deleting}
					placeholder={me?.email ?? ''}
					class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-4)] px-3 py-2 text-ui text-[var(--color-text-bright)] outline-none placeholder:text-[var(--color-text-muted)] transition-[color,border-color,box-shadow] duration-150 focus:border-[var(--color-red)] focus:shadow-[0_0_0_2px_rgba(207,129,114,0.1)] disabled:opacity-60"
				/>
			</div>

			<div class="mt-6 flex justify-end gap-3">
				<button
					onclick={() => showDeleteConfirm = false}
					disabled={deleting}
					class="rounded-[var(--radius-button)] border border-[var(--color-border)] px-4 py-2 text-ui text-[var(--color-text-secondary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)] disabled:opacity-50"
				>
					Cancel
				</button>
				<button
					onclick={handleDeleteAccount}
					disabled={deleting || deleteConfirmation !== me?.email}
					class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-red)] px-5 py-2 text-ui font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0 disabled:opacity-50 disabled:hover:translate-y-0"
				>
					{#if deleting}
						<svg class="animate-spin" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 12a9 9 0 1 1-6.219-8.56" /></svg>
					{/if}
					Delete account
				</button>
			</div>
		</div>
	</div>
{/if}

<style>
	/* ── Checkmark draw animation (mirrors CopyButton pattern) ── */
	.check-draw {
		animation: checkScale 0.3s cubic-bezier(0.25, 1, 0.5, 1) both;
	}
	:global(.check-draw polyline) {
		stroke-dasharray: 24;
		stroke-dashoffset: 24;
		animation: checkStroke 0.3s cubic-bezier(0.25, 1, 0.5, 1) 0.05s forwards;
	}
	@keyframes checkScale {
		0% { transform: scale(0.6); opacity: 0; }
		50% { opacity: 1; }
		100% { transform: scale(1); opacity: 1; }
	}
	@keyframes checkStroke {
		to { stroke-dashoffset: 0; }
	}

	/* ── Avatar hover ring ── */
	.avatar-ring {
		transition: border-color 0.2s ease, box-shadow 0.2s ease;
	}
	.avatar-ring:hover {
		border-color: var(--color-accent-mid);
		box-shadow: 0 0 0 3px var(--color-accent-glow-mid);
	}

	/* ── Dialog backdrop fade ── */
	.backdrop-fade {
		animation: backdropIn 0.2s ease both;
	}
	@keyframes backdropIn {
		from { opacity: 0; }
		to { opacity: 1; }
	}

	/* ── Respect reduced motion ── */
	@media (prefers-reduced-motion: reduce) {
		.check-draw,
		.avatar-ring,
		.backdrop-fade {
			animation-duration: 0.01ms !important;
			animation-iteration-count: 1 !important;
			transition-duration: 0.01ms !important;
		}
		:global(.check-draw polyline) {
			animation-duration: 0.01ms !important;
			animation-iteration-count: 1 !important;
		}
	}
</style>
