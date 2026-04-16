<script lang="ts">
	import { goto } from '$app/navigation';
	import { onMount, onDestroy } from 'svelte';
	import { page } from '$app/stores';
	import { auth } from '$lib/auth.svelte';
	import { teams } from '$lib/teams.svelte';
	import { apiLogin, apiSignup } from '$lib/api/auth';
	import {
		IconGithub,
		IconMail,
		IconLock,
		IconUser,
		IconEye,
		IconEyeOff
	} from '$lib/components/icons';

	let mode: 'signin' | 'signup' = $state('signin');
	let email = $state('');
	let password = $state('');
	let confirmPassword = $state('');
	let name = $state('');
	let showPassword = $state(false);
	let error = $state('');
	let loading = $state(false);
	let signupDone = $state(false);

	const oauthErrorMessages: Record<string, string> = {
		account_deactivated: 'Your account has been deactivated — contact the administrator to regain access',
		access_denied: 'Access was denied by the provider',
		email_taken: 'An account with this email already exists',
		exchange_failed: 'Authentication failed — please try again',
	};

	// Read OAuth error forwarded from /auth/github/callback
	onMount(() => {
		if (auth.isAuthenticated) {
			goto('/dashboard');
			return;
		}

		const urlErr = $page.url.searchParams.get('error');
		if (urlErr) {
			const decoded = decodeURIComponent(urlErr);
			error = oauthErrorMessages[decoded] ?? decoded;
		}
	});

	// Mouse-reactive glow — moves opposite to cursor with viscous drag
	let glowX = $state(50);
	let glowY = $state(50);
	let targetX = 50;
	let targetY = 50;
	let rafId: number | null = null;

	const LERP_FACTOR = 0.04;

	function lerpLoop() {
		const dx = targetX - glowX;
		const dy = targetY - glowY;

		if (Math.abs(dx) > 0.01 || Math.abs(dy) > 0.01) {
			glowX += dx * LERP_FACTOR;
			glowY += dy * LERP_FACTOR;
			rafId = requestAnimationFrame(lerpLoop);
		} else {
			glowX = targetX;
			glowY = targetY;
			rafId = null;
		}
	}

	function handleMouseMove(e: MouseEvent) {
		const target = e.currentTarget as HTMLElement;
		const rect = target.getBoundingClientRect();
		const normX = (e.clientX - rect.left) / rect.width;
		const normY = (e.clientY - rect.top) / rect.height;

		targetX = 55 - normX * 10;
		targetY = 55 - normY * 10;

		if (rafId === null) {
			rafId = requestAnimationFrame(lerpLoop);
		}
	}

	onDestroy(() => {
		if (rafId !== null) cancelAnimationFrame(rafId);
	});

	const title = $derived(mode === 'signin' ? 'Welcome back' : 'Create account');
	const subtitle = $derived(
		mode === 'signin' ? 'Sign in to your Wrenn account' : 'Get started with Wrenn'
	);
	const submitLabel = $derived(mode === 'signin' ? 'Sign in' : 'Create account');
	const switchText = $derived(
		mode === 'signin' ? "Don't have an account?" : 'Already have an account?'
	);
	const switchAction = $derived(mode === 'signin' ? 'Sign up' : 'Sign in');

	function switchMode() {
		mode = mode === 'signin' ? 'signup' : 'signin';
		error = '';
		name = '';
		confirmPassword = '';
		signupDone = false;
	}

	async function handleSubmit(e: Event) {
		e.preventDefault();
		error = '';
		loading = true;

		if (mode === 'signup') {
			if (password !== confirmPassword) {
				error = 'Passwords do not match.';
				loading = false;
				return;
			}
			if (password.length < 8) {
				error = 'Password must be at least 8 characters.';
				loading = false;
				return;
			}

			const result = await apiSignup(email, password, name);
			loading = false;

			if (!result.ok) {
				error = result.error;
				return;
			}

			signupDone = true;
			return;
		}

		// Sign in
		const result = await apiLogin(email, password);
		loading = false;

		if (!result.ok) {
			error = result.error;
			return;
		}

		teams.reset();
		auth.login(result.data);
		goto('/dashboard');
	}
</script>

<svelte:head>
	<title>Wrenn — {mode === 'signin' ? 'Sign in' : 'Sign up'}</title>
</svelte:head>

<div class="flex min-h-screen">
	<!-- Left panel — branding -->
	<!-- svelte-ignore a11y_no_static_element_interactions -->
	<div
		class="relative hidden w-1/2 flex-col items-center justify-center overflow-hidden bg-[var(--color-bg-1)] lg:flex"
		onmousemove={handleMouseMove}
	>
		<!-- Dot grid texture — industrial depth layer -->
		<div
			class="pointer-events-none absolute inset-0 opacity-60"
			style="background-image: radial-gradient(circle, rgba(94,140,88,0.09) 1px, transparent 1px); background-size: 24px 24px;"
			aria-hidden="true"
		></div>

		<!-- Mouse-reactive radial glow — renders above dot grid -->
		<div
			class="pointer-events-none absolute inset-0"
			style="background: radial-gradient(ellipse 60% 50% at {glowX}% {glowY}%, rgba(94, 140, 88, 0.18) 0%, transparent 70%)"
			aria-hidden="true"
		></div>

		<!-- Centered logo + wordmark -->
		<div
			class="relative z-10 flex flex-col items-center"
			style="animation: fadeUp 0.35s ease both"
		>
			<img src="/logo.svg" alt="Wrenn" class="h-20 w-20 rounded-[var(--radius-card)]" />
			<span
				class="mt-5 font-brand text-[3.143rem] text-[var(--color-text-bright)]"
			>
				Wrenn
			</span>
		</div>

		<!-- Tagline below logo — larger, more commanding -->
		<div
			class="relative z-10 mt-14 max-w-[460px] text-center"
			style="animation: fadeUp 0.35s ease 0.1s both"
		>
			<h1
				class="font-serif text-[6.5rem] leading-[0.95] tracking-[-0.06em] text-[var(--color-text-bright)]"
			>
				Scale Up.<br /><span class="text-[var(--color-accent-bright)]">Spin Out.</span>
			</h1>
		</div>

		<!-- Sub-tagline -->
		<p
			class="relative z-10 mt-10 font-mono text-ui uppercase tracking-[0.1em] text-[var(--color-text-tertiary)]"
			style="animation: fadeUp 0.35s ease 0.2s both"
		>
			Isolated VMs. Milliseconds to live.
		</p>
	</div>

	<!-- Right panel — auth form -->
	<div
		class="flex w-full flex-col items-center justify-center bg-[var(--color-bg-0)] px-6 lg:w-1/2"
	>
		<!-- Mobile logo (shown only on small screens) -->
		<div
			class="mb-10 flex flex-col items-center lg:hidden"
			style="animation: fadeUp 0.35s ease both"
		>
			<img src="/logo.svg" alt="Wrenn" class="h-12 w-12 rounded-[var(--radius-card)]" />
			<span
				class="mt-2 font-brand text-page text-[var(--color-text-bright)]"
			>
				Wrenn
			</span>
		</div>

		<div class="w-full max-w-[400px]" style="animation: fadeUp 0.35s ease 0.1s both">
			{#if signupDone}
				<!-- Post-signup confirmation -->
				<div class="rounded-[var(--radius-card)] border border-[var(--color-border)] bg-[var(--color-bg-1)] p-6" style="animation: fadeUp 0.3s ease both">
					<h2 class="font-serif text-display text-[var(--color-text-bright)]">Check your email</h2>
					<p class="mt-2 text-body text-[var(--color-text-secondary)]">
						We've sent an activation link to <span class="font-medium text-[var(--color-text-bright)]">{email}</span>. Click the link to activate your account.
					</p>
					<p class="mt-4 text-ui text-[var(--color-text-tertiary)]">
						The link expires in 30 minutes. If you don't see it, check your spam folder.
					</p>
					<button
						type="button"
						onclick={switchMode}
						class="mt-6 w-full rounded-[var(--radius-button)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] px-4 py-3 text-body font-medium text-[var(--color-text-bright)] transition-all duration-150 hover:border-[var(--color-accent)]"
					>
						Back to sign in
					</button>
				</div>
			{:else}
				<!-- Header -->
				<div class="mb-8">
					<h2
						class="font-serif text-display tracking-[0.01em] text-[var(--color-text-bright)]"
					>
						{title}
					</h2>
					<p class="mt-2 text-body text-[var(--color-text-secondary)]">
						{subtitle}
					</p>
				</div>

				<!-- GitHub OAuth -->
				<a
					href="/api/auth/oauth/github"
					class="flex w-full items-center justify-center gap-2.5 rounded-[var(--radius-button)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] px-4 py-3 text-body font-medium text-[var(--color-text-bright)] no-underline transition-all duration-150 hover:border-[var(--color-accent)] hover:text-[var(--color-text-bright)]"
				>
					<IconGithub size={16} />
					Continue with GitHub
				</a>

				<!-- Divider -->
				<div class="my-6 flex items-center gap-3">
					<div class="h-px flex-1 bg-[var(--color-border)]"></div>
					<span
						class="font-mono text-badge uppercase tracking-[0.1em] text-[var(--color-text-muted)]"
						>or</span
					>
					<div class="h-px flex-1 bg-[var(--color-border)]"></div>
				</div>

				<!-- Form -->
				<form onsubmit={handleSubmit} class="space-y-3">
					{#if mode === 'signup'}
						<div class="group relative">
							<div
								class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-[var(--color-text-muted)] transition-colors duration-150 group-focus-within:text-[var(--color-accent)]"
							>
								<IconUser size={14} />
							</div>
							<input
								type="text"
								bind:value={name}
								placeholder="Full name"
								autocomplete="name"
								class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-2)] py-3 pl-9 pr-3 text-body text-[var(--color-text-bright)] outline-none transition-all duration-150 placeholder:text-[var(--color-text-muted)] focus:border-[var(--color-accent)]"
							/>
						</div>
					{/if}
					<div class="group relative">
						<div
							class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-[var(--color-text-muted)] transition-colors duration-150 group-focus-within:text-[var(--color-accent)]"
						>
							<IconMail size={14} />
						</div>
						<input
							type="email"
							bind:value={email}
							placeholder="Email address"
							autocomplete="email"
							class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-2)] py-3 pl-9 pr-3 text-body text-[var(--color-text-bright)] outline-none transition-all duration-150 placeholder:text-[var(--color-text-muted)] focus:border-[var(--color-accent)]"
						/>
					</div>

					<div class="group relative">
						<div
							class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-[var(--color-text-muted)] transition-colors duration-150 group-focus-within:text-[var(--color-accent)]"
						>
							<IconLock size={14} />
						</div>
						<input
							type={showPassword ? 'text' : 'password'}
							bind:value={password}
							placeholder="Password"
							autocomplete={mode === 'signin' ? 'current-password' : 'new-password'}
							class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-2)] py-3 pl-9 pr-10 text-body text-[var(--color-text-bright)] outline-none transition-all duration-150 placeholder:text-[var(--color-text-muted)] focus:border-[var(--color-accent)]"
						/>
						<button
							type="button"
							onclick={() => (showPassword = !showPassword)}
							class="absolute right-3 top-1/2 -translate-y-1/2 text-[var(--color-text-muted)] transition-colors duration-150 hover:text-[var(--color-text-secondary)]"
							tabindex={-1}
						>
							{#if showPassword}
								<IconEyeOff size={14} />
							{:else}
								<IconEye size={14} />
							{/if}
						</button>
					</div>

					{#if mode === 'signup'}
						<div class="group relative">
							<div
								class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-[var(--color-text-muted)] transition-colors duration-150 group-focus-within:text-[var(--color-accent)]"
							>
								<IconLock size={14} />
							</div>
							<input
								type="password"
								bind:value={confirmPassword}
								placeholder="Confirm password"
								autocomplete="new-password"
								class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-2)] py-3 pl-9 pr-3 text-body text-[var(--color-text-bright)] outline-none transition-all duration-150 placeholder:text-[var(--color-text-muted)] focus:border-[var(--color-accent)]"
							/>
						</div>
					{/if}

					{#if mode === 'signin'}
						<div class="flex justify-end">
							<a
								href="/forgot-password"
								class="text-ui text-[var(--color-text-secondary)] transition-colors duration-150 hover:text-[var(--color-accent-mid)]"
							>
								Forgot password?
							</a>
						</div>
					{/if}

					{#if error}
						<p class="text-ui text-[var(--color-red)]">{error}</p>
					{/if}

					<button
						type="submit"
						disabled={loading}
						class="!mt-5 w-full rounded-[var(--radius-button)] bg-[var(--color-accent)] px-4 py-3 text-body font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0 disabled:pointer-events-none disabled:opacity-50"
					>
						{#if loading}
							<span class="inline-flex items-center gap-2">
								<span
									class="inline-block h-3.5 w-3.5 animate-spin rounded-full border-2 border-white/30 border-t-white"
								></span>
								{submitLabel}
							</span>
						{:else}
							{submitLabel}
						{/if}
					</button>
				</form>

				<!-- Switch mode -->
				<p class="mt-6 text-center text-ui text-[var(--color-text-secondary)]">
					{switchText}
					<button
						type="button"
						onclick={switchMode}
						class="font-medium text-[var(--color-text-primary)] transition-colors duration-150 hover:text-[var(--color-text-bright)]"
					>
						{switchAction}
					</button>
				</p>
			{/if}
		</div>
	</div>
</div>
