<script lang="ts">
	import type { BuildStep } from '$lib/build-console-ws.svelte';

	type Props = {
		steps: BuildStep[];
	};
	let { steps }: Props = $props();

	// [keyword, rest] split of a recipe instruction.
	function splitInstruction(cmd: string): [string, string] {
		const idx = cmd.indexOf(' ');
		if (idx === -1) return [cmd.toUpperCase(), ''];
		return [cmd.slice(0, idx).toUpperCase(), cmd.slice(idx + 1)];
	}

	function keywordColor(keyword: string): string {
		switch (keyword) {
			case 'RUN':
				return 'var(--color-blue)';
			case 'START':
				return 'var(--color-accent-bright)';
			case 'ENV':
				return 'var(--color-amber)';
			case 'USER':
				return 'var(--color-accent)';
			case 'COPY':
				return 'var(--color-text-bright)';
			case 'WORKDIR':
				return 'var(--color-text-tertiary)';
			case 'HEALTHCHECK':
				return 'var(--color-accent-bright)';
			default:
				return 'var(--color-text-muted)';
		}
	}

	function formatMs(ms: number): string {
		if (ms < 1000) return `${ms}ms`;
		return `${(ms / 1000).toFixed(ms < 10000 ? 1 : 0)}s`;
	}
</script>

<div class="flex items-center justify-between border-b border-[var(--color-border)] px-4 py-2.5">
	<span class="text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">
		Steps
	</span>
	{#if steps.length > 0}
		<span class="font-mono text-label tabular-nums text-[var(--color-text-muted)]">
			{steps.length}
		</span>
	{/if}
</div>

<div class="min-h-0 flex-1 overflow-y-auto">
	{#each steps as s (s.step)}
		{@const [kw, rest] = splitInstruction(s.cmd)}
		<div class="border-b border-[var(--color-border)] px-4 py-2.5">
			<div class="flex items-center gap-2.5">
				{#if s.status === 'running'}
					<span class="relative flex h-2 w-2 shrink-0">
						<span
							class="animate-status-ping absolute inline-flex h-full w-full rounded-full bg-[var(--color-blue)] opacity-60"
						></span>
						<span class="relative inline-flex h-2 w-2 rounded-full bg-[var(--color-blue)]"></span>
					</span>
				{:else if s.status === 'success'}
					<svg
						class="shrink-0"
						width="13"
						height="13"
						viewBox="0 0 24 24"
						fill="none"
						stroke="var(--color-accent-bright)"
						stroke-width="2.5"
						stroke-linecap="round"
						stroke-linejoin="round"
						aria-hidden="true"
					>
						<polyline points="20 6 9 17 4 12" />
					</svg>
				{:else}
					<svg
						class="shrink-0"
						width="13"
						height="13"
						viewBox="0 0 24 24"
						fill="none"
						stroke="var(--color-red)"
						stroke-width="2.5"
						stroke-linecap="round"
						stroke-linejoin="round"
						aria-hidden="true"
					>
						<line x1="18" y1="6" x2="6" y2="18" />
						<line x1="6" y1="6" x2="18" y2="18" />
					</svg>
				{/if}
				<span class="font-mono text-label tabular-nums text-[var(--color-text-muted)]">
					{s.step}
				</span>
				<div class="flex-1"></div>
				{#if s.exit !== null && s.exit !== 0}
					<span
						class="rounded-full bg-[var(--color-red)]/10 px-1.5 py-0.5 font-mono text-label text-[var(--color-red)]"
					>
						exit {s.exit}
					</span>
				{/if}
				{#if s.elapsedMs !== null}
					<span class="font-mono text-label tabular-nums text-[var(--color-text-muted)]">
						{formatMs(s.elapsedMs)}
					</span>
				{/if}
			</div>
			<code class="mt-1.5 block truncate font-mono text-meta">
				<span style="color: {keywordColor(kw)}">{kw}</span>{#if rest}{' '}<span
						class="text-[var(--color-text-secondary)]">{rest}</span>{/if}
			</code>
		</div>
	{/each}

	{#if steps.length === 0}
		<div class="px-4 py-8 text-center text-meta text-[var(--color-text-muted)]">
			Waiting for the first step...
		</div>
	{/if}
</div>
