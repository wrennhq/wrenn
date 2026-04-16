<script lang="ts">
	import { toast } from '$lib/toast.svelte';
</script>

<div class="pointer-events-none fixed bottom-6 right-6 z-[100] flex flex-col-reverse gap-2">
	{#each toast.list as t (t.id)}
		<div
			class="pointer-events-auto flex min-w-[280px] max-w-[400px] items-start gap-3 rounded-[var(--radius-card)] border bg-[var(--color-bg-2)] px-4 py-3 text-ui {t.type === 'error'
				? 'border-[var(--color-red)]/30 text-[var(--color-red)]'
				: 'border-[var(--color-accent)]/30 text-[var(--color-accent-bright)]'}"
			style="animation: fadeUp 0.2s ease both"
		>
			<span class="flex-1 leading-relaxed">{t.message}</span>
			<button
				onclick={() => toast.dismiss(t.id)}
				aria-label="Dismiss"
				class="mt-0.5 shrink-0 opacity-50 transition-opacity hover:opacity-100"
			>
				<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
					<line x1="18" y1="6" x2="6" y2="18" /><line x1="6" y1="6" x2="18" y2="18" />
				</svg>
			</button>
		</div>
	{/each}
</div>
