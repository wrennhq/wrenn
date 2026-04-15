<script lang="ts">
	import { destroyCapsule } from '$lib/api/capsules';
	import type { ApiResult } from '$lib/api/client';

	type Props = {
		open: boolean;
		capsuleId: string;
		onclose: () => void;
		ondestroyed?: () => void;
		destroyFn?: (id: string) => Promise<ApiResult<void>>;
	};
	let { open, capsuleId, onclose, ondestroyed, destroyFn }: Props = $props();

	let destroying = $state(false);
	let error = $state<string | null>(null);

	async function handleDestroy() {
		destroying = true;
		error = null;
		const destroy = destroyFn ?? destroyCapsule;
		const result = await destroy(capsuleId);
		if (result.ok) {
			error = null;
			ondestroyed?.();
			onclose();
		} else {
			error = result.error;
		}
		destroying = false;
	}

	function handleClose() {
		if (!destroying) {
			error = null;
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
		<div class="relative w-full max-w-[380px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)]" style="animation: fadeUp 0.2s ease both; box-shadow: var(--shadow-dialog)">
			<div class="p-6">
			<h2 class="font-serif text-heading text-[var(--color-text-bright)]">Destroy Capsule</h2>
			<p class="mt-2 text-ui text-[var(--color-text-tertiary)]">
				Terminate <span class="font-mono text-[var(--color-text-secondary)]">{capsuleId}</span> and destroy all data inside it. This cannot be undone.
			</p>

			{#if error}
				<div class="mt-4 rounded-[var(--radius-input)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-3 py-2 text-meta text-[var(--color-red)]">
					{error}
				</div>
			{/if}

			<div class="mt-6 flex justify-end gap-3">
				<button
					onclick={handleClose}
					disabled={destroying}
					class="rounded-[var(--radius-button)] border border-[var(--color-border)] px-4 py-2 text-ui text-[var(--color-text-secondary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)] disabled:opacity-50"
				>
					Cancel
				</button>
				<button
					onclick={handleDestroy}
					disabled={destroying}
					class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-red)] px-5 py-2 text-ui font-semibold text-white transition-all duration-150 hover:brightness-110 hover:-translate-y-px active:translate-y-0 disabled:opacity-50 disabled:hover:translate-y-0"
				>
					{#if destroying}
						<svg class="animate-spin" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
							<path d="M21 12a9 9 0 1 1-6.219-8.56" />
						</svg>
						Destroying...
					{:else}
						Destroy
					{/if}
				</button>
			</div>
			</div>
		</div>
	</div>
{/if}
