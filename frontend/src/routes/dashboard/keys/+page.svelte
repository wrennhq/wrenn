<script lang="ts">
	import Sidebar from '$lib/components/Sidebar.svelte';
	import { onMount } from 'svelte';
	import { listKeys, createKey, revokeKey, type APIKey } from '$lib/api/keys';
	import { toast } from '$lib/toast.svelte';
	import { formatDate, timeAgo } from '$lib/utils/format';

	let collapsed = $state(
		typeof window !== 'undefined'
			? localStorage.getItem('wrenn_sidebar_collapsed') === 'true'
			: false
	);

	// List state
	let keys = $state<APIKey[]>([]);
	let loading = $state(true);
	let error = $state<string | null>(null);

	// Create dialog state
	let showCreate = $state(false);
	let createName = $state('');
	let creating = $state(false);
	let createError = $state<string | null>(null);

	// Reveal state — shown immediately after creation
	let newKey = $state<APIKey | null>(null);
	let copied = $state(false);
	let copyCount = $state(0); // increment to re-trigger bounce animation

	// Delight: flash the row for a key once its reveal dialog is dismissed
	let flashKeyId = $state<string | null>(null);

	// Revoke state
	let revokeTarget = $state<APIKey | null>(null);
	let revoking = $state(false);
	let revokeError = $state<string | null>(null);

	async function fetchKeys() {
		loading = true;
		error = null;
		const result = await listKeys();
		if (result.ok) {
			keys = result.data;
		} else {
			error = result.error;
		}
		loading = false;
	}

	async function handleCreate() {
		if (!createName.trim()) return;
		creating = true;
		createError = null;
		const result = await createKey(createName.trim());
		if (result.ok) {
			keys = [result.data, ...keys];
			newKey = result.data;
			showCreate = false;
			createName = '';
			copied = false;
			copyCount = 0;
		} else {
			createError = result.error;
		}
		creating = false;
	}

	async function handleRevoke() {
		if (!revokeTarget) return;
		revoking = true;
		revokeError = null;
		const id = revokeTarget.id;
		const result = await revokeKey(id);
		if (result.ok) {
			keys = keys.filter((k) => k.id !== id);
			revokeTarget = null;
		} else {
			revokeError = result.error;
		}
		revoking = false;
	}

	function dismissReveal() {
		const id = newKey?.id ?? null;
		newKey = null;
		if (id) {
			flashKeyId = id;
			setTimeout(() => { flashKeyId = null; }, 1600);
		}
	}

	async function copyKey() {
		if (!newKey?.key) return;
		try {
			await navigator.clipboard.writeText(newKey.key);
			copied = true;
			copyCount += 1;
			setTimeout(() => (copied = false), 2000);
		} catch {
			toast.error('Copy failed — select the key text and copy it manually.');
		}
	}

	onMount(fetchKeys);
</script>

<svelte:head>
	<title>Wrenn — API Keys</title>
</svelte:head>

<div class="flex h-screen overflow-hidden">
	<Sidebar bind:collapsed />

	<div class="flex flex-1 flex-col overflow-hidden">
		<main class="flex-1 overflow-y-auto bg-[var(--color-bg-0)]">
			<!-- Header -->
			<div class="px-7 pt-8">
				<div class="flex items-center justify-between">
					<div>
						<h1 class="font-serif text-page tracking-[-0.02em] text-[var(--color-text-bright)]">
							API Keys
						</h1>
						<p class="mt-2 text-ui text-[var(--color-text-secondary)]">
							Bearer tokens for the Wrenn API and SDKs. Each key grants full access — guard it like a password.
						</p>
					</div>

					<button
						onclick={() => { showCreate = true; createError = null; createName = ''; }}
						class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-accent)] px-4 py-2 text-ui font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0"
					>
						<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
							<line x1="12" y1="5" x2="12" y2="19" /><line x1="5" y1="12" x2="19" y2="12" />
						</svg>
						New Key
					</button>
				</div>

				<div class="mt-6 border-b border-[var(--color-border)]"></div>
			</div>

			<!-- Content -->
			<div class="p-8" style="animation: fadeUp 0.35s ease both">
				{#if error}
					<div class="mb-4 flex items-center justify-between gap-4 rounded-[var(--radius-card)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-4 py-3 text-ui text-[var(--color-red)]">
					<span>{error}</span>
					<button
						onclick={fetchKeys}
						class="shrink-0 font-semibold underline-offset-2 hover:underline"
					>
						Try again
					</button>
					</div>
				{/if}

				{#if loading}
					<div class="flex items-center justify-center py-24">
						<div class="flex items-center gap-3 text-ui text-[var(--color-text-secondary)]">
							<svg class="animate-spin" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
								<path d="M21 12a9 9 0 1 1-6.219-8.56" />
							</svg>
							Loading keys...
						</div>
					</div>
				{:else if keys.length === 0}
					<div class="flex flex-col items-center justify-center py-[72px]">
						<div class="relative mb-5">
							<div class="absolute inset-0 -m-4 rounded-full" style="background: radial-gradient(circle, rgba(94,140,88,0.08) 0%, transparent 70%)"></div>
							<div class="relative flex h-14 w-14 items-center justify-center rounded-[var(--radius-card)] border border-[var(--color-accent)]/20 bg-[var(--color-bg-3)]" style="animation: iconFloat 4s ease-in-out infinite">
								<svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="var(--color-accent-mid)" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
									<path d="M21 2l-2 2m-7.61 7.61a5.5 5.5 0 1 1-7.778 7.778 5.5 5.5 0 0 1 7.777-7.777zm0 0L15.5 7.5m0 0l3 3L22 7l-3-3m-3.5 3.5L19 4" />
								</svg>
							</div>
						</div>
						<p class="font-serif text-heading tracking-[-0.02em] text-[var(--color-text-bright)]">No API keys yet</p>
						<p class="mt-1.5 text-ui text-[var(--color-text-tertiary)]">Nothing can call the API without a key. Create one to authenticate your SDK or HTTP requests.</p>
						<button
							onclick={() => { showCreate = true; createError = null; createName = ''; }}
							class="mt-6 flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-accent)] px-5 py-2.5 text-ui font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0"
						>
							New Key
							<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
								<line x1="12" y1="5" x2="12" y2="19" /><line x1="5" y1="12" x2="19" y2="12" />
							</svg>
						</button>
					</div>
				{:else}
					<div class="rounded-[var(--radius-card)] border border-[var(--color-border)] overflow-hidden">
						<!-- Table header -->
						<div class="grid grid-cols-[2fr_1.2fr_1.4fr_1.4fr_80px] border-b border-[var(--color-border)] bg-[var(--color-bg-3)]">
							<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Key</div>
							<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Created By</div>
							<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Created</div>
							<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Last Used</div>
							<div class="px-5 py-3 text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]"></div>
						</div>

						{#each keys as key, i (key.id)}
							<div
								class="key-row relative grid grid-cols-[2fr_1.2fr_1.4fr_1.4fr_80px] items-center overflow-hidden border-b border-[var(--color-border)] transition-colors duration-150 hover:bg-[var(--color-bg-3)] last:border-b-0 {flashKeyId === key.id ? 'key-born' : ''}"
								style="animation: fadeUp 0.35s ease both; animation-delay: {i * 40}ms"
							>
								<div class="row-stripe pointer-events-none absolute left-0 top-0 h-full w-0.5 bg-[var(--color-accent)]"></div>
								<!-- Name + prefix -->
								<div class="min-w-0 flex flex-col gap-1 px-5 py-4">
									<span class="truncate text-ui font-medium text-[var(--color-text-bright)]">{key.name || '—'}</span>
									<span class="inline-flex w-fit items-center rounded-sm border border-[var(--color-border-mid)] bg-[var(--color-bg-4)] px-1.5 py-0.5 font-mono text-badge text-[var(--color-accent-mid)]">{key.key_prefix}…</span>
								</div>

								<!-- Created by -->
								<div class="min-w-0 px-5 py-4">
									<span class="block truncate text-ui text-[var(--color-text-secondary)]">{key.creator_email ?? key.created_by}</span>
								</div>

								<!-- Created at -->
								<div class="px-5 py-4">
									<span class="text-ui text-[var(--color-text-secondary)]">{formatDate(key.created_at)}</span>
								</div>

								<!-- Last used -->
								<div class="px-5 py-4">
									{#if key.last_used}
										<span class="text-ui text-[var(--color-text-secondary)]" title={formatDate(key.last_used)}>
											{timeAgo(key.last_used)}
										</span>
									{:else}
										<span class="text-ui text-[var(--color-text-muted)]">Never</span>
									{/if}
								</div>

								<!-- Revoke -->
								<div class="flex justify-end px-5 py-4">
									<button
										onclick={() => { revokeTarget = key; revokeError = null; }}
										class="rounded-[var(--radius-button)] border border-[var(--color-border)] px-2.5 py-1 text-label font-semibold uppercase tracking-[0.04em] text-[var(--color-text-tertiary)] transition-colors duration-150 hover:border-[var(--color-red)]/40 hover:text-[var(--color-red)]"
									>
										Revoke
									</button>
								</div>
							</div>
						{/each}
					</div>

					<p class="mt-3 text-meta text-[var(--color-text-muted)]">
						{keys.length} {keys.length === 1 ? 'key' : 'keys'} total
					</p>
				{/if}
			</div>
		</main>

		<footer class="h-px shrink-0 bg-[var(--color-border)]"></footer>
	</div>
</div>

<!-- Create Key Dialog -->
{#if showCreate}
	<div class="fixed inset-0 z-50 flex items-center justify-center">
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="absolute inset-0 bg-black/60"
			onclick={() => { if (!creating) showCreate = false; }}
			onkeydown={(e) => { if (e.key === 'Escape' && !creating) showCreate = false; }}
		></div>

		<div class="relative w-full max-w-[420px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] p-6" style="animation: fadeUp 0.2s ease both; box-shadow: var(--shadow-dialog)">
			<h2 class="font-serif text-heading tracking-[-0.02em] text-[var(--color-text-bright)]">New API Key</h2>
			<p class="mt-1 text-ui text-[var(--color-text-tertiary)]">Name it after its environment or purpose — production, staging, CI. You can't rename it later.</p>

			{#if createError}
				<div class="mt-4 rounded-[var(--radius-input)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-3 py-2 text-meta text-[var(--color-red)]">
					{createError}
				</div>
			{/if}

			<div class="mt-5">
				<label class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]" for="key-name">
					Key name
				</label>
				<input
					id="key-name"
					type="text"
					placeholder="e.g. Production SDK"
					bind:value={createName}
					onkeydown={(e) => { if (e.key === 'Enter' && !creating) handleCreate(); }}
					class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-4)] px-3 py-2 text-ui text-[var(--color-text-bright)] outline-none placeholder:text-[var(--color-text-muted)] transition-colors duration-150 focus:border-[var(--color-accent)]"
				/>
			</div>

			<div class="mt-6 flex justify-end gap-3">
				<button
					onclick={() => { showCreate = false; }}
					disabled={creating}
					class="rounded-[var(--radius-button)] border border-[var(--color-border)] px-4 py-2 text-ui text-[var(--color-text-secondary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)] disabled:opacity-50"
				>
					Cancel
				</button>
				<button
					onclick={handleCreate}
					disabled={creating || !createName.trim()}
					class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-accent)] px-5 py-2 text-ui font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0 disabled:opacity-50 disabled:hover:translate-y-0"
				>
					{#if creating}
						<svg class="animate-spin" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
							<path d="M21 12a9 9 0 1 1-6.219-8.56" />
						</svg>
						Creating...
					{:else}
						Create Key
					{/if}
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Key Reveal Dialog — shown once after creation -->
{#if newKey}
	<div class="fixed inset-0 z-50 flex items-center justify-center">
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="absolute inset-0 bg-black/60"
			onclick={dismissReveal}
			onkeydown={(e) => { if (e.key === 'Escape') dismissReveal(); }}
		></div>

		<div class="relative w-full max-w-[480px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] p-6" style="animation: fadeUp 0.2s ease both; box-shadow: var(--shadow-dialog)">
			<!-- Success indicator -->
			<div class="mb-4 flex items-center gap-2.5">
				<span class="flex h-5 w-5 shrink-0 items-center justify-center rounded-full bg-[var(--color-accent-glow-mid)]" style="animation: circlePop 0.4s cubic-bezier(0.34, 1.56, 0.64, 1) both">
					<svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="var(--color-accent-bright)" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
						<polyline points="20 6 9 17 4 12" style="stroke-dasharray: 24; animation: checkDraw 0.35s 0.2s ease both" />
					</svg>
				</span>
				<span class="text-meta font-semibold text-[var(--color-accent-mid)]" style="animation: fadeUp 0.3s 0.15s ease both">Key created successfully</span>
			</div>

			<h2 class="font-serif text-heading tracking-[-0.02em] text-[var(--color-text-bright)]">{newKey.name || 'API Key'}</h2>
			<p class="mt-1 text-ui text-[var(--color-text-tertiary)]">
				Copy this key now — it won't be shown again.
			</p>

			<!-- Key display -->
			<div class="mt-5 rounded-[var(--radius-input)] border bg-[var(--color-bg-0)] p-4" style="animation: keyRevealGlow 1.4s 0.1s ease-out both">
				<div class="flex items-center gap-3">
					<span class="min-w-0 flex-1 break-all font-mono text-ui leading-relaxed text-[var(--color-text-bright)]">
						{newKey.key ?? ''}
					</span>
					{#key copyCount}
						<button
							onclick={copyKey}
							style={copied ? 'animation: copyBounce 0.35s cubic-bezier(0.34, 1.56, 0.64, 1) both' : ''}
							class="shrink-0 flex items-center gap-1.5 rounded-[var(--radius-button)] border px-3 py-1.5 text-meta font-semibold transition-all duration-150
								{copied
									? 'border-[var(--color-accent)]/40 bg-[var(--color-accent-glow-mid)] text-[var(--color-accent-mid)]'
									: 'border-[var(--color-border-mid)] text-[var(--color-text-secondary)] hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)]'}"
						>
							{#if copied}
								<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
									<polyline points="20 6 9 17 4 12" />
								</svg>
								Copied
							{:else}
								<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
									<rect x="9" y="9" width="13" height="13" rx="2" ry="2" />
									<path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1" />
								</svg>
								Copy
							{/if}
						</button>
					{/key}
				</div>
			</div>

			<!-- Warning -->
			<div class="mt-3 flex items-start gap-2 rounded-[var(--radius-input)] border border-[var(--color-amber)]/20 bg-[var(--color-amber)]/5 px-3 py-2.5">
				<svg class="mt-0.5 shrink-0" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="var(--color-amber)" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
					<path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z" />
					<line x1="12" y1="9" x2="12" y2="13" /><line x1="12" y1="17" x2="12.01" y2="17" />
				</svg>
				<p class="text-meta leading-relaxed text-[var(--color-amber)]">
					This is shown once. Store it in your secrets manager — not a note, not a chat message, not a commit.
				</p>
			</div>

			<div class="mt-6 flex justify-end">
				<button
					onclick={dismissReveal}
					class="rounded-[var(--radius-button)] bg-[var(--color-bg-4)] border border-[var(--color-border-mid)] px-5 py-2 text-ui font-semibold text-[var(--color-text-primary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:bg-[var(--color-bg-5)]"
				>
					Done
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Revoke Confirmation Dialog -->
{#if revokeTarget}
	<div class="fixed inset-0 z-50 flex items-center justify-center">
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="absolute inset-0 bg-black/60"
			onclick={() => { if (!revoking) revokeTarget = null; }}
			onkeydown={(e) => { if (e.key === 'Escape' && !revoking) revokeTarget = null; }}
		></div>

		<div class="relative w-full max-w-[380px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] p-6" style="animation: fadeUp 0.2s ease both; box-shadow: var(--shadow-dialog)">
			<h2 class="font-serif text-heading tracking-[-0.02em] text-[var(--color-text-bright)]">Revoke Key</h2>
			<p class="mt-2 text-ui text-[var(--color-text-tertiary)]">
				Permanently revoke <span class="font-medium text-[var(--color-text-secondary)]">{revokeTarget.name || revokeTarget.id}</span>.
				Any request using this key will fail immediately.
			</p>
			<span class="mt-2 inline-flex items-center rounded-sm border border-[var(--color-border-mid)] bg-[var(--color-bg-4)] px-1.5 py-0.5 font-mono text-badge text-[var(--color-text-muted)]">{revokeTarget.key_prefix}…</span>

			{#if revokeError}
				<div class="mt-4 rounded-[var(--radius-input)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-3 py-2 text-meta text-[var(--color-red)]">
					{revokeError}
				</div>
			{/if}

			<div class="mt-6 flex justify-end gap-3">
				<button
					onclick={() => { revokeTarget = null; }}
					disabled={revoking}
					class="rounded-[var(--radius-button)] border border-[var(--color-border)] px-4 py-2 text-ui text-[var(--color-text-secondary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)] disabled:opacity-50"
				>
					Cancel
				</button>
				<button
					onclick={handleRevoke}
					disabled={revoking}
					class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-red)] px-5 py-2 text-ui font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0 disabled:opacity-50 disabled:hover:translate-y-0"
				>
					{#if revoking}
						<svg class="animate-spin" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
							<path d="M21 12a9 9 0 1 1-6.219-8.56" />
						</svg>
						Revoking...
					{:else}
						Revoke Key
					{/if}
				</button>
			</div>
		</div>
	</div>
{/if}

<style>
	/* Checkmark stroke draw — plays on reveal dialog open */
	@keyframes checkDraw {
		from { stroke-dashoffset: 24; }
		to   { stroke-dashoffset: 0; }
	}

	/* Success circle scales in with a spring overshoot */
	@keyframes circlePop {
		from { transform: scale(0); opacity: 0; }
		60%  { transform: scale(1.18); opacity: 1; }
		to   { transform: scale(1);    opacity: 1; }
	}

	/* Key display area pulses accent border on dialog open — draws eye to "copy this" */
	@keyframes keyRevealGlow {
		0%   { border-color: var(--color-accent); box-shadow: 0 0 0 3px rgba(94,140,88,0.16); }
		60%  { border-color: var(--color-accent); box-shadow: 0 0 0 3px rgba(94,140,88,0.08); }
		100% { border-color: var(--color-border-mid); box-shadow: none; }
	}

	/* Copy button spring bounce on successful copy */
	@keyframes copyBounce {
		0%   { transform: scale(1);    }
		40%  { transform: scale(1.12); }
		100% { transform: scale(1);    }
	}

	/* Row born flash — matches capsule-born pattern */
	@keyframes key-born {
		0%, 25% { background-color: rgba(94, 140, 88, 0.1); }
		100%    { background-color: transparent; }
	}
	.key-born {
		animation: key-born 1.6s ease-out forwards;
	}

	/* Left accent stripe — slides in on row hover */
	.row-stripe {
		transform: scaleY(0);
		transform-origin: center;
		transition: transform 0.18s cubic-bezier(0.25, 1, 0.5, 1);
	}
	.key-row:hover .row-stripe {
		transform: scaleY(1);
	}
</style>
