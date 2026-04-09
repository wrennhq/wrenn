<script lang="ts">
	import { createCapsule, type Capsule, type CreateCapsuleParams } from '$lib/api/capsules';

	type Props = {
		open: boolean;
		onclose: () => void;
		oncreated?: (capsule: Capsule) => void;
	};
	let { open, onclose, oncreated }: Props = $props();

	let createForm = $state<CreateCapsuleParams>({ template: 'minimal', vcpus: 1, memory_mb: 512, timeout_sec: 0 });
	let creating = $state(false);
	let createError = $state<string | null>(null);

	async function handleCreate() {
		creating = true;
		createError = null;
		const result = await createCapsule(createForm);
		if (result.ok) {
			createForm = { template: 'minimal', vcpus: 1, memory_mb: 512, timeout_sec: 0 };
			oncreated?.(result.data);
			onclose();
		} else {
			createError = result.error;
		}
		creating = false;
	}
</script>

{#if open}
	<div class="fixed inset-0 z-50 flex items-center justify-center">
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="absolute inset-0 bg-black/60"
			onclick={() => { if (!creating) onclose(); }}
			onkeydown={(e) => { if (e.key === 'Escape' && !creating) onclose(); }}
		></div>

		<div class="relative w-full max-w-[420px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] p-6" style="animation: fadeUp 0.2s ease both; box-shadow: var(--shadow-dialog)">
			<h2 class="font-serif text-heading tracking-[-0.02em] text-[var(--color-text-bright)]">Launch Capsule</h2>
			<p class="mt-1 text-ui text-[var(--color-text-tertiary)]">Configure resources and launch. The VM will be ready in under a second.</p>

			{#if createError}
				<div class="mt-4 rounded-[var(--radius-input)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-3 py-2 text-meta text-[var(--color-red)]">
					{createError}
				</div>
			{/if}

			<div class="mt-5 space-y-4">
				<div>
					<label class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]" for="create-template">Template</label>
					<input
						id="create-template"
						type="text"
						bind:value={createForm.template}
						class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-4)] px-3 py-2 font-mono text-ui text-[var(--color-text-bright)] outline-none placeholder:text-[var(--color-text-muted)] transition-colors duration-150 focus:border-[var(--color-accent)]"
						placeholder="minimal"
					/>
					<p class="mt-1.5 text-meta text-[var(--color-text-muted)]">Name of a snapshot or base image to boot from.</p>
				</div>

				<div class="grid grid-cols-2 gap-3">
					<div>
						<label class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]" for="create-vcpus">vCPUs</label>
						<input
							id="create-vcpus"
							type="number"
							min="1"
							max="8"
							bind:value={createForm.vcpus}
							class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-4)] px-3 py-2 font-mono text-ui text-[var(--color-text-bright)] outline-none transition-colors duration-150 focus:border-[var(--color-accent)]"
						/>
					</div>
					<div>
						<label class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]" for="create-memory">Memory (MB)</label>
						<input
							id="create-memory"
							type="number"
							min="128"
							max="8192"
							step="128"
							bind:value={createForm.memory_mb}
							class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-4)] px-3 py-2 font-mono text-ui text-[var(--color-text-bright)] outline-none transition-colors duration-150 focus:border-[var(--color-accent)]"
						/>
					</div>
				</div>

				<div>
					<label class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]" for="create-timeout">Idle timeout</label>
					<input
						id="create-timeout"
						type="number"
						min="0"
						bind:value={createForm.timeout_sec}
						class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-4)] px-3 py-2 font-mono text-ui text-[var(--color-text-bright)] outline-none transition-colors duration-150 focus:border-[var(--color-accent)]"
						placeholder="0"
					/>
					<p class="mt-1.5 text-meta text-[var(--color-text-muted)]">Seconds of inactivity before the capsule pauses. Set to 0 to keep it running indefinitely.</p>
				</div>
			</div>

			<div class="mt-6 flex justify-end gap-3">
				<button
					onclick={onclose}
					disabled={creating}
					class="rounded-[var(--radius-button)] border border-[var(--color-border)] px-4 py-2 text-ui text-[var(--color-text-secondary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)] disabled:opacity-50"
				>
					Cancel
				</button>
				<button
					onclick={handleCreate}
					disabled={creating}
					class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-accent)] px-5 py-2 text-ui font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0 disabled:opacity-50 disabled:hover:translate-y-0"
				>
					{#if creating}
						<svg class="animate-spin" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
							<path d="M21 12a9 9 0 1 1-6.219-8.56" />
						</svg>
						Launching...
					{:else}
						Launch
					{/if}
				</button>
			</div>
		</div>
	</div>
{/if}
