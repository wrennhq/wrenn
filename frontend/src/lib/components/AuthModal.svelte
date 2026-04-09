<script lang="ts">
	import { Dialog } from 'bits-ui';
	import {
		IconGithub,
		IconMail,
		IconLock,
		IconUser,
		IconX,
		IconEye,
		IconEyeOff
	} from './icons';

	let {
		mode = $bindable('signin'),
		open = $bindable(false),
		onSwitchMode
	}: {
		mode: 'signin' | 'signup';
		open: boolean;
		onSwitchMode: () => void;
	} = $props();

	let email = $state('');
	let password = $state('');
	let name = $state('');
	let showPassword = $state(false);

	const title = $derived(mode === 'signin' ? 'Welcome back' : 'Create account');
	const subtitle = $derived(
		mode === 'signin' ? 'Sign in to your Wrenn account' : 'Get started with Wrenn'
	);
	const submitLabel = $derived(mode === 'signin' ? 'Sign in' : 'Create account');
	const switchText = $derived(
		mode === 'signin' ? "Don't have an account?" : 'Already have an account?'
	);
	const switchAction = $derived(mode === 'signin' ? 'Sign up' : 'Sign in');

	function handleSubmit(e: Event) {
		e.preventDefault();
	}
</script>

<Dialog.Root bind:open>
	<Dialog.Portal>
		<Dialog.Overlay
			class="fixed inset-0 z-50 bg-black/70 backdrop-blur-[3px]"
			style="animation: overlayFadeIn 200ms ease"
		/>
		<Dialog.Content
			class="fixed left-1/2 top-1/2 z-50 w-[calc(100%-2rem)] max-w-[400px] -translate-x-1/2 -translate-y-1/2 rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] p-0"
			style="animation: contentSlideIn 250ms cubic-bezier(0.16, 1, 0.3, 1)"
		>
			<!-- Close button -->
			<Dialog.Close
				class="absolute right-3 top-3 flex h-7 w-7 items-center justify-center rounded-[var(--radius-button)] border border-transparent text-[var(--color-text-tertiary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:bg-[var(--color-bg-4)] hover:text-[var(--color-text-secondary)]"
			>
				<IconX size={14} />
			</Dialog.Close>

			<div class="px-7 pb-7 pt-8">
				<!-- Header -->
				<div class="mb-7">
					<Dialog.Title
						class="font-serif text-page tracking-[-0.02em] text-[var(--color-text-bright)]"
					>
						{title}
					</Dialog.Title>
					<Dialog.Description
						class="mt-1 text-ui text-[var(--color-text-secondary)]"
					>
						{subtitle}
					</Dialog.Description>
				</div>

				<!-- GitHub OAuth -->
				<button
					type="button"
					class="flex w-full items-center justify-center gap-2.5 rounded-[var(--radius-button)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] px-4 py-2.5 text-ui font-medium text-[var(--color-text-bright)] transition-all duration-150 hover:border-[var(--color-accent)] hover:text-[var(--color-text-bright)]"
				>
					<IconGithub size={16} />
					Continue with GitHub
				</button>

				<!-- Divider -->
				<div class="my-5 flex items-center gap-3">
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
								class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-2)] py-2.5 pl-9 pr-3 text-ui text-[var(--color-text-bright)] outline-none transition-all duration-150 placeholder:text-[var(--color-text-muted)] focus:border-[var(--color-accent)]"
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
							class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-2)] py-2.5 pl-9 pr-3 text-ui text-[var(--color-text-bright)] outline-none transition-all duration-150 placeholder:text-[var(--color-text-muted)] focus:border-[var(--color-accent)]"
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
							class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-2)] py-2.5 pl-9 pr-10 text-ui text-[var(--color-text-bright)] outline-none transition-all duration-150 placeholder:text-[var(--color-text-muted)] focus:border-[var(--color-accent)]"
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

					{#if mode === 'signin'}
						<div class="flex justify-end">
							<button
								type="button"
								class="text-meta text-[var(--color-text-secondary)] transition-colors duration-150 hover:text-[var(--color-accent-mid)]"
							>
								Forgot password?
							</button>
						</div>
					{/if}

					<button
						type="submit"
						class="!mt-5 w-full rounded-[var(--radius-button)] bg-[var(--color-accent)] px-4 py-2.5 text-ui font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0"
					>
						{submitLabel}
					</button>
				</form>

				<!-- Switch mode -->
				<p class="mt-5 text-center text-meta text-[var(--color-text-secondary)]">
					{switchText}
					<button
						type="button"
						onclick={onSwitchMode}
						class="font-medium text-[var(--color-text-primary)] transition-colors duration-150 hover:text-[var(--color-text-bright)]"
					>
						{switchAction}
					</button>
				</p>
			</div>
		</Dialog.Content>
	</Dialog.Portal>
</Dialog.Root>

<style>
	@keyframes overlayFadeIn {
		from {
			opacity: 0;
		}
		to {
			opacity: 1;
		}
	}

	@keyframes contentSlideIn {
		from {
			opacity: 0;
			transform: translate(-50%, -48%) scale(0.96);
		}
		to {
			opacity: 1;
			transform: translate(-50%, -50%) scale(1);
		}
	}
</style>
