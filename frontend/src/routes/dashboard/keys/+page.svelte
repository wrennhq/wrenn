<script lang="ts">
	import Sidebar from '$lib/components/Sidebar.svelte';
	import { onMount } from 'svelte';
	import { listKeys, createKey, revokeKey, type APIKey } from '$lib/api/keys';

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

	async function copyKey() {
		if (!newKey?.key) return;
		await navigator.clipboard.writeText(newKey.key);
		copied = true;
		setTimeout(() => (copied = false), 2000);
	}

	function formatDate(iso: string | undefined): string {
		if (!iso) return '—';
		return new Date(iso).toLocaleString('en-US', {
			month: 'short',
			day: 'numeric',
			year: 'numeric',
			hour: '2-digit',
			minute: '2-digit',
			hour12: false
		});
	}

	function timeAgo(iso: string | undefined): string {
		if (!iso) return '';
		const seconds = Math.floor((Date.now() - new Date(iso).getTime()) / 1000);
		if (seconds < 60) return `${seconds}s ago`;
		if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
		if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
		return `${Math.floor(seconds / 86400)}d ago`;
	}


	onMount(fetchKeys);
</script>

<svelte:head>
	<title>Wrenn - API Keys</title>
</svelte:head>

<div class="flex h-screen overflow-hidden">
	<Sidebar bind:collapsed />

	<div class="flex flex-1 flex-col overflow-hidden">
		<main class="flex-1 overflow-y-auto bg-[var(--color-bg-0)]">
			<!-- Header -->
			<div class="px-7 pt-6">
				<div class="flex items-center justify-between">
					<div>
						<h1 class="font-serif text-[24px] tracking-[-0.02em] text-[var(--color-text-bright)]">
							API Keys
						</h1>
						<p class="mt-1 text-[13px] text-[var(--color-text-tertiary)]">
							Keys authenticate SDK and direct API requests. Treat them like passwords.
						</p>
					</div>

					<button
						onclick={() => { showCreate = true; createError = null; createName = ''; }}
						class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-accent)] px-4 py-2 text-[13px] font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0"
					>
						<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
							<line x1="12" y1="5" x2="12" y2="19" /><line x1="5" y1="12" x2="19" y2="12" />
						</svg>
						New Key
					</button>
				</div>

				<div class="mt-5 border-b border-[var(--color-border)]"></div>
			</div>

			<!-- Content -->
			<div class="p-7" style="animation: fadeUp 0.35s ease both">
				{#if error}
					<div class="mb-4 rounded-[var(--radius-card)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-4 py-3 text-[13px] text-[var(--color-red)]">
						{error}
					</div>
				{/if}

				{#if loading}
					<div class="flex items-center justify-center py-24">
						<div class="flex items-center gap-3 text-[13px] text-[var(--color-text-secondary)]">
							<svg class="animate-spin" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
								<path d="M21 12a9 9 0 1 1-6.219-8.56" />
							</svg>
							Loading keys...
						</div>
					</div>
				{:else if keys.length === 0}
					<div class="flex flex-col items-center justify-center py-[72px]">
						<div class="mb-5 flex h-14 w-14 items-center justify-center rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-3)]">
							<svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="var(--color-text-secondary)" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
								<path d="M21 2l-2 2m-7.61 7.61a5.5 5.5 0 1 1-7.778 7.778 5.5 5.5 0 0 1 7.777-7.777zm0 0L15.5 7.5m0 0l3 3L22 7l-3-3m-3.5 3.5L19 4" />
							</svg>
						</div>
						<p class="font-serif text-[20px] tracking-[-0.02em] text-[var(--color-text-bright)]">No API keys yet</p>
						<p class="mt-1.5 text-[13px] text-[var(--color-text-tertiary)]">Create a key to authenticate SDK and API requests.</p>
						<button
							onclick={() => { showCreate = true; createError = null; createName = ''; }}
							class="mt-6 flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-accent)] px-5 py-2.5 text-[13px] font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0"
						>
							Create a Key
							<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
								<line x1="12" y1="5" x2="12" y2="19" /><line x1="5" y1="12" x2="19" y2="12" />
							</svg>
						</button>
					</div>
				{:else}
					<div class="rounded-[var(--radius-card)] border border-[var(--color-border)] overflow-hidden">
						<!-- Table header -->
						<div class="grid grid-cols-[2fr_1.2fr_1.4fr_1.4fr_80px] border-b border-[var(--color-border)] bg-[var(--color-bg-3)]">
							<div class="px-4 py-[11px] text-[11px] font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Name / Key</div>
							<div class="px-4 py-[11px] text-[11px] font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Created By</div>
							<div class="px-4 py-[11px] text-[11px] font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Created</div>
							<div class="px-4 py-[11px] text-[11px] font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]">Last Used</div>
							<div class="px-4 py-[11px] text-[11px] font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)]"></div>
						</div>

						{#each keys as key, i (key.id)}
							<div
								class="grid grid-cols-[2fr_1.2fr_1.4fr_1.4fr_80px] items-center border-b border-[var(--color-border)] transition-colors duration-150 hover:bg-[var(--color-bg-3)] last:border-b-0"
								style="animation: fadeUp 0.35s ease both; animation-delay: {i * 40}ms"
							>
								<!-- Name + prefix -->
								<div class="flex flex-col gap-0.5 px-4 py-3">
									<span class="text-[13px] font-medium text-[var(--color-text-bright)]">{key.name || '—'}</span>
									<span class="font-mono text-[12px] text-[var(--color-text-muted)]">{key.key_prefix}...</span>
								</div>

								<!-- Created by -->
								<div class="px-4 py-3">
									<span class="text-[13px] text-[var(--color-text-secondary)]">{key.creator_email ?? key.created_by}</span>
								</div>

								<!-- Created at -->
								<div class="px-4 py-3">
									<span class="text-[13px] text-[var(--color-text-secondary)]">{formatDate(key.created_at)}</span>
								</div>

								<!-- Last used -->
								<div class="px-4 py-3">
									{#if key.last_used}
										<span class="text-[13px] text-[var(--color-text-secondary)]" title={formatDate(key.last_used)}>
											{timeAgo(key.last_used)}
										</span>
									{:else}
										<span class="text-[13px] text-[var(--color-text-muted)]">Never</span>
									{/if}
								</div>

								<!-- Revoke -->
								<div class="flex justify-end px-4 py-3">
									<button
										onclick={() => { revokeTarget = key; revokeError = null; }}
										class="rounded-[var(--radius-button)] border border-[var(--color-border)] px-2.5 py-1 text-[11px] font-semibold uppercase tracking-[0.04em] text-[var(--color-text-tertiary)] transition-colors duration-150 hover:border-[var(--color-red)]/40 hover:text-[var(--color-red)]"
									>
										Revoke
									</button>
								</div>
							</div>
						{/each}
					</div>

					<p class="mt-3 text-[12px] text-[var(--color-text-muted)]">
						{keys.length} {keys.length === 1 ? 'key' : 'keys'} total
					</p>
				{/if}
			</div>
		</main>

		<!-- Status bar -->
		<footer class="flex h-7 shrink-0 items-center justify-end border-t border-[var(--color-border)] bg-[var(--color-bg-1)] px-7">
			<div class="flex items-center gap-1.5">
				<span class="inline-flex h-[5px] w-[5px] rounded-full bg-[var(--color-accent)]"></span>
				<span class="font-mono text-[11px] uppercase tracking-[0.04em] text-[var(--color-text-secondary)]">All systems operational</span>
			</div>
		</footer>
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

		<div class="relative w-full max-w-[400px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] p-6" style="animation: fadeUp 0.2s ease both">
			<h2 class="font-serif text-[20px] tracking-[-0.02em] text-[var(--color-text-bright)]">New API Key</h2>
			<p class="mt-1 text-[13px] text-[var(--color-text-tertiary)]">Give your key a name to identify it later.</p>

			{#if createError}
				<div class="mt-4 rounded-[var(--radius-input)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-3 py-2 text-[12px] text-[var(--color-red)]">
					{createError}
				</div>
			{/if}

			<div class="mt-5">
				<label class="mb-1.5 block text-[11px] font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]" for="key-name">
					Key name
				</label>
				<input
					id="key-name"
					type="text"
					placeholder="e.g. Production SDK"
					bind:value={createName}
					onkeydown={(e) => { if (e.key === 'Enter' && !creating) handleCreate(); }}
					class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-4)] px-3 py-2 text-[13px] text-[var(--color-text-bright)] outline-none placeholder:text-[var(--color-text-muted)] transition-colors duration-150 focus:border-[var(--color-accent)]"
				/>
			</div>

			<div class="mt-6 flex justify-end gap-3">
				<button
					onclick={() => { showCreate = false; }}
					disabled={creating}
					class="rounded-[var(--radius-button)] border border-[var(--color-border)] px-4 py-2 text-[13px] text-[var(--color-text-secondary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)] disabled:opacity-50"
				>
					Cancel
				</button>
				<button
					onclick={handleCreate}
					disabled={creating || !createName.trim()}
					class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-accent)] px-5 py-2 text-[13px] font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0 disabled:opacity-50 disabled:hover:translate-y-0"
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
			onclick={() => { newKey = null; }}
			onkeydown={(e) => { if (e.key === 'Escape') newKey = null; }}
		></div>

		<div class="relative w-full max-w-[480px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] p-6" style="animation: fadeUp 0.2s ease both">
			<!-- Success indicator -->
			<div class="mb-4 flex items-center gap-2.5">
				<span class="flex h-5 w-5 items-center justify-center rounded-full bg-[var(--color-accent-glow-mid)]">
					<svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="var(--color-accent-bright)" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
						<polyline points="20 6 9 17 4 12" />
					</svg>
				</span>
				<span class="text-[12px] font-semibold text-[var(--color-accent-mid)]">Key created successfully</span>
			</div>

			<h2 class="font-serif text-[20px] tracking-[-0.02em] text-[var(--color-text-bright)]">{newKey.name || 'API Key'}</h2>
			<p class="mt-1 text-[13px] text-[var(--color-text-tertiary)]">
				Copy this key now — it won't be shown again.
			</p>

			<!-- Key display -->
			<div class="mt-5 rounded-[var(--radius-input)] border border-[var(--color-border-mid)] bg-[var(--color-bg-0)] p-4">
				<div class="flex items-center gap-3">
					<span class="min-w-0 flex-1 break-all font-mono text-[13px] leading-relaxed text-[var(--color-text-bright)]">
						{newKey.key ?? ''}
					</span>
					<button
						onclick={copyKey}
						class="shrink-0 flex items-center gap-1.5 rounded-[var(--radius-button)] border px-3 py-1.5 text-[12px] font-semibold transition-all duration-150
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
				</div>
			</div>

			<!-- Warning -->
			<div class="mt-3 flex items-start gap-2 rounded-[var(--radius-input)] border border-[var(--color-amber)]/20 bg-[var(--color-amber)]/5 px-3 py-2.5">
				<svg class="mt-0.5 shrink-0" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="var(--color-amber)" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
					<path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z" />
					<line x1="12" y1="9" x2="12" y2="13" /><line x1="12" y1="17" x2="12.01" y2="17" />
				</svg>
				<p class="text-[12px] leading-relaxed text-[var(--color-amber)]">
					Store this key securely. For security reasons, we only show it once and cannot retrieve it later.
				</p>
			</div>

			<div class="mt-6 flex justify-end">
				<button
					onclick={() => { newKey = null; }}
					class="rounded-[var(--radius-button)] bg-[var(--color-bg-4)] border border-[var(--color-border-mid)] px-5 py-2 text-[13px] font-semibold text-[var(--color-text-primary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:bg-[var(--color-bg-5)]"
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

		<div class="relative w-full max-w-[380px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] p-6" style="animation: fadeUp 0.2s ease both">
			<h2 class="font-serif text-[20px] tracking-[-0.02em] text-[var(--color-text-bright)]">Revoke Key</h2>
			<p class="mt-2 text-[13px] text-[var(--color-text-tertiary)]">
				Revoke <span class="font-medium text-[var(--color-text-secondary)]">{revokeTarget.name || revokeTarget.id}</span>?
				Any request using it will stop working immediately.
			</p>
			<p class="mt-1.5 font-mono text-[12px] text-[var(--color-text-muted)]">{revokeTarget.key_prefix}...</p>

			{#if revokeError}
				<div class="mt-4 rounded-[var(--radius-input)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-3 py-2 text-[12px] text-[var(--color-red)]">
					{revokeError}
				</div>
			{/if}

			<div class="mt-6 flex justify-end gap-3">
				<button
					onclick={() => { revokeTarget = null; }}
					disabled={revoking}
					class="rounded-[var(--radius-button)] border border-[var(--color-border)] px-4 py-2 text-[13px] text-[var(--color-text-secondary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)] disabled:opacity-50"
				>
					Cancel
				</button>
				<button
					onclick={handleRevoke}
					disabled={revoking}
					class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-red)] px-5 py-2 text-[13px] font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0 disabled:opacity-50 disabled:hover:translate-y-0"
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
