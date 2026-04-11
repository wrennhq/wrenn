<script lang="ts">
	import { createSnapshot } from '$lib/api/capsules';

	type Props = {
		open: boolean;
		capsuleId: string;
		pauseFirst?: boolean;
		onclose: () => void;
		onsnapshot?: () => void;
	};
	let { open, capsuleId, pauseFirst = false, onclose, onsnapshot }: Props = $props();

	let snapshotName = $state('');
	let snapshotting = $state(false);
	let error = $state<string | null>(null);

	function reset() {
		snapshotName = '';
		error = null;
	}

	async function handleConfirm() {
		snapshotting = true;
		error = null;
		const result = await createSnapshot(capsuleId, snapshotName.trim() || undefined);
		if (result.ok) {
			reset();
			onsnapshot?.();
			onclose();
		} else {
			error = result.error;
		}
		snapshotting = false;
	}

	function handleClose() {
		if (!snapshotting) {
			reset();
			onclose();
		}
	}
</script>

{#if open}
	<div class="fixed inset-0 z-50 flex items-center justify-center">
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="absolute inset-0 bg-black/60"
			onclick={handleClose}
			onkeydown={(e) => { if (e.key === 'Escape') handleClose(); }}
		></div>

		<div class="relative w-full max-w-[420px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] overflow-hidden" style="animation: fadeUp 0.2s ease both; box-shadow: var(--shadow-dialog)">
			<div class="flex items-center gap-4 border-b border-[var(--color-border)] bg-[var(--color-bg-3)] px-6 py-5">
				<div class="flex h-10 w-10 shrink-0 items-center justify-center rounded-[var(--radius-input)] bg-[var(--color-accent)]/15 text-[var(--color-accent)] shadow-[0_0_12px_var(--color-accent-glow)]">
					<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round">
						<path d="M14.5 4h-5L7 7H2v13a2 2 0 002 2h16a2 2 0 002-2V7h-5l-2.5-3z" />
						<circle cx="12" cy="15" r="3" />
					</svg>
				</div>
				<div>
					<h2 class="font-serif text-heading tracking-[-0.02em] text-[var(--color-text-bright)]">Capture snapshot</h2>
					<p class="mt-0.5 text-meta text-[var(--color-text-muted)] font-mono">{capsuleId}</p>
				</div>
			</div>

			<div class="px-6 pt-5 pb-6 space-y-4">
				{#if pauseFirst}
					<div class="flex items-start gap-2.5 rounded-[var(--radius-input)] border border-[var(--color-amber)]/25 bg-[var(--color-amber)]/8 px-3 py-2.5">
						<svg class="mt-px shrink-0 text-[var(--color-amber)]" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
							<path d="M10.29 3.86L1.82 18a2 2 0 001.71 3h16.94a2 2 0 001.71-3L13.71 3.86a2 2 0 00-3.42 0z" />
							<line x1="12" y1="9" x2="12" y2="13" />
							<line x1="12" y1="17" x2="12.01" y2="17" />
						</svg>
						<p class="text-meta text-[var(--color-amber)] leading-relaxed">This capsule will be <strong class="font-semibold">paused first</strong>, then its full state (memory + disk) will be captured.</p>
					</div>
				{:else}
					<p class="text-ui text-[var(--color-text-tertiary)]">The capsule's current state (memory + disk) will be captured and stored as a reusable snapshot.</p>
				{/if}

				{#if error}
					<div class="rounded-[var(--radius-input)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-3 py-2 text-meta text-[var(--color-red)]">
						{error}
					</div>
				{/if}

				<div>
					<div class="mb-1.5 flex items-baseline justify-between">
						<label class="text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]" for="snapshot-name">Snapshot name</label>
						<span class="text-meta text-[var(--color-text-muted)]">optional</span>
					</div>
					<input
						id="snapshot-name"
						type="text"
						bind:value={snapshotName}
						disabled={snapshotting}
						class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-4)] px-3 py-2 font-mono text-ui text-[var(--color-text-bright)] outline-none placeholder:text-[var(--color-text-muted)] transition-colors duration-150 focus:border-[var(--color-accent)] disabled:opacity-50"
						placeholder="e.g. after-apt-install, pre-migration"
						onkeydown={(e) => { if (e.key === 'Enter' && !snapshotting) handleConfirm(); }}
					/>
					<p class="mt-1.5 text-meta text-[var(--color-text-muted)]">Leave blank to use an auto-generated name.</p>
				</div>

				<div class="flex justify-end gap-3 pt-1">
					<button
						onclick={handleClose}
						disabled={snapshotting}
						class="rounded-[var(--radius-button)] border border-[var(--color-border)] px-4 py-2 text-ui text-[var(--color-text-secondary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)] disabled:opacity-50"
					>
						Cancel
					</button>
					<button
						onclick={handleConfirm}
						disabled={snapshotting}
						class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-accent)] px-5 py-2 text-ui font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0 disabled:opacity-50 disabled:hover:translate-y-0"
					>
						{#if snapshotting}
							<svg class="animate-spin" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
								<path d="M21 12a9 9 0 1 1-6.219-8.56" />
							</svg>
							Capturing...
						{:else}
							Capture snapshot
						{/if}
					</button>
				</div>
			</div>
		</div>
	</div>
{/if}
