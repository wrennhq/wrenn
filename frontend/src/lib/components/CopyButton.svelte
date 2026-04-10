<script lang="ts">
	let { value }: { value: string } = $props();

	let copied = $state(false);
	let timer: ReturnType<typeof setTimeout> | null = null;

	async function copy(e: MouseEvent) {
		e.preventDefault();
		e.stopPropagation();
		try {
			await navigator.clipboard.writeText(value);
			copied = true;
			if (timer) clearTimeout(timer);
			timer = setTimeout(() => (copied = false), 1800);
		} catch {
			// Clipboard API unavailable
		}
	}
</script>

<button
	onclick={copy}
	class="copy-btn"
	class:copied
	aria-label="Copy to clipboard"
>
	<span class="copy-btn-inner">
		{#if copied}
			<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round" class="check-icon">
				<polyline points="20 6 9 17 4 12" />
			</svg>
		{:else}
			<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="clipboard-icon">
				<rect x="9" y="9" width="13" height="13" rx="2" ry="2" />
				<path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1" />
			</svg>
		{/if}
	</span>


</button>

<style>
	.copy-btn {
		position: relative;
		display: inline-flex;
		align-items: center;
		gap: 4px;
		height: 22px;
		padding: 0 4px;
		border-radius: 4px;
		color: var(--color-text-muted);
		background: transparent;
		border: 1px solid transparent;
		cursor: pointer;
		transition: color 0.15s ease, background 0.15s ease, border-color 0.15s ease;
		flex-shrink: 0;
	}

	.copy-btn:hover {
		color: var(--color-text-secondary);
		background: var(--color-bg-4);
		border-color: var(--color-border);
	}

	.copy-btn:active {
		transform: scale(0.92);
	}

	/* ── Copied state ── */
	.copy-btn.copied {
		opacity: 1;
		color: var(--color-accent-bright);
		background: rgba(94, 140, 88, 0.1);
		border-color: rgba(94, 140, 88, 0.25);
	}

	.copy-btn-inner {
		display: flex;
		align-items: center;
		justify-content: center;
		width: 14px;
		height: 14px;
	}

	/* ── Clipboard icon — subtle nudge on hover ── */
	.clipboard-icon {
		transition: transform 0.15s ease;
	}
	.copy-btn:hover .clipboard-icon {
		transform: translate(-0.5px, -0.5px);
	}

	/* ── Check icon draw animation ── */
	.check-icon {
		animation: checkDraw 0.3s cubic-bezier(0.25, 1, 0.5, 1) both;
	}
	.check-icon polyline {
		stroke-dasharray: 24;
		stroke-dashoffset: 24;
		animation: drawCheck 0.3s cubic-bezier(0.25, 1, 0.5, 1) 0.05s forwards;
	}
	@keyframes drawCheck {
		to { stroke-dashoffset: 0; }
	}
	@keyframes checkDraw {
		0% { transform: scale(0.6); opacity: 0; }
		50% { opacity: 1; }
		100% { transform: scale(1); opacity: 1; }
	}

</style>
