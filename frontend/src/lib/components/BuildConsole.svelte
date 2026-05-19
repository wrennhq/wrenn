<script lang="ts">
	import { onMount, onDestroy, tick } from 'svelte';
	import type { Build } from '$lib/api/builds';
	import { createBuildConsole } from '$lib/build-console-ws.svelte';
	import BuildStepList from './BuildStepList.svelte';

	type Props = {
		buildId: string;
		build: Build;
		onStatusChange?: (status: string) => void;
	};
	let { buildId, build, onStatusChange }: Props = $props();

	const bc = createBuildConsole(buildId);

	let containerRef = $state<HTMLDivElement>();
	// eslint-disable-next-line @typescript-eslint/no-explicit-any
	let term: any = null;
	// eslint-disable-next-line @typescript-eslint/no-explicit-any
	let fitAddon: any = null;
	let resizeObserver: ResizeObserver | null = null;
	let fitDebounce: ReturnType<typeof setTimeout> | null = null;
	let alive = true;

	const stepTotal = $derived(bc.totalSteps || build.total_steps);

	const TERM_THEME = {
		background: '#0a0c0b',
		foreground: '#d0cdc6',
		cursor: '#0a0c0b',
		cursorAccent: '#0a0c0b',
		selectionBackground: 'rgba(94, 140, 88, 0.25)',
		selectionForeground: '#eae7e2',
		black: '#1a1e1c',
		red: '#cf8172',
		green: '#5e8c58',
		yellow: '#d4a73c',
		blue: '#5a9fd4',
		magenta: '#b07ab8',
		cyan: '#5aafb0',
		white: '#d0cdc6',
		brightBlack: '#454340',
		brightRed: '#e09585',
		brightGreen: '#89a785',
		brightYellow: '#e0c070',
		brightBlue: '#7ab8e0',
		brightMagenta: '#c898cf',
		brightCyan: '#7ac5c6',
		brightWhite: '#eae7e2'
	};

	// Propagate live build status to the parent (drives the cancel button).
	$effect(() => {
		if (bc.buildStatus) onStatusChange?.(bc.buildStatus);
	});

	function connLabel(state: string): string {
		switch (state) {
			case 'connected':
				return 'Live';
			case 'connecting':
				return 'Connecting';
			case 'closed':
				return 'Ended';
			default:
				return 'Disconnected';
		}
	}

	onMount(async () => {
		const [{ Terminal }, { FitAddon }] = await Promise.all([
			import('@xterm/xterm'),
			import('@xterm/addon-fit')
		]);
		await import('@xterm/xterm/css/xterm.css');
		await tick();
		// The component may have been destroyed during the awaits above.
		if (!alive || !containerRef) return;

		fitAddon = new FitAddon();
		term = new Terminal({
			disableStdin: true,
			cursorBlink: false,
			cursorStyle: 'underline',
			fontFamily: "'JetBrains Mono Variable', 'JetBrains Mono', monospace",
			fontSize: 13,
			lineHeight: 1.4,
			theme: TERM_THEME,
			scrollback: 10000,
			convertEol: true
		});
		term.loadAddon(fitAddon);
		term.open(containerRef);
		requestAnimationFrame(() => fitAddon?.fit());

		resizeObserver = new ResizeObserver(() => {
			if (fitDebounce) clearTimeout(fitDebounce);
			fitDebounce = setTimeout(() => fitAddon?.fit(), 50);
		});
		resizeObserver.observe(containerRef);

		bc.connect((text) => term?.write(text));
	});

	onDestroy(() => {
		alive = false;
		if (fitDebounce) clearTimeout(fitDebounce);
		resizeObserver?.disconnect();
		bc.disconnect();
		term?.dispose();
	});
</script>

<div class="flex min-h-0 flex-1 flex-col">
	<!-- Console toolbar -->
	<div
		class="flex items-center gap-3 border-b border-[var(--color-border)] bg-[var(--color-bg-1)] px-4 py-2"
	>
		<span class="font-mono text-label text-[var(--color-text-muted)]">{buildId}</span>
		<span class="font-mono text-label tabular-nums text-[var(--color-text-tertiary)]">
			{bc.currentStep}/{stepTotal}
		</span>
		<div class="flex-1"></div>
		{#if bc.connState === 'connected'}
			<span class="relative flex h-[7px] w-[7px]">
				<span
					class="animate-status-ping absolute inline-flex h-full w-full rounded-full bg-[var(--color-accent)]"
				></span>
				<span class="relative inline-flex h-[7px] w-[7px] rounded-full bg-[var(--color-accent)]"></span>
			</span>
		{:else if bc.connState === 'connecting'}
			<svg
				class="animate-spin text-[var(--color-text-tertiary)]"
				width="11"
				height="11"
				viewBox="0 0 24 24"
				fill="none"
				stroke="currentColor"
				stroke-width="2.5"
				aria-hidden="true"
			>
				<path d="M21 12a9 9 0 1 1-6.219-8.56" />
			</svg>
		{:else}
			<span
				class="h-[7px] w-[7px] rounded-full {bc.connState === 'error'
					? 'bg-[var(--color-red)]'
					: 'bg-[var(--color-text-muted)]'}"
			></span>
		{/if}
		<span class="text-label text-[var(--color-text-tertiary)]">{connLabel(bc.connState)}</span>
	</div>

	<!-- Terminal + step list -->
	<div class="flex min-h-0 flex-1">
		<div class="relative min-w-0 flex-1 bg-[var(--color-bg-0)]">
			<div bind:this={containerRef} class="terminal-host absolute inset-0"></div>
		</div>
		<aside
			class="flex w-72 shrink-0 flex-col border-l border-[var(--color-border)] bg-[var(--color-bg-1)]"
		>
			<BuildStepList steps={bc.steps} />
		</aside>
	</div>

	{#if bc.errorMessage}
		<div
			class="border-t border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-4 py-2 font-mono text-meta text-[var(--color-red)]"
		>
			{bc.errorMessage}
		</div>
	{/if}
</div>

<style>
	.terminal-host :global(.xterm) {
		padding: 12px 4px 12px 16px;
		height: 100%;
	}
	.terminal-host :global(.xterm-viewport),
	.terminal-host :global(.xterm-screen) {
		background-color: #0a0c0b !important;
	}
	.terminal-host :global(.xterm-viewport) {
		scrollbar-width: thin;
		scrollbar-color: rgba(94, 140, 88, 0.18) transparent;
	}
	.terminal-host :global(.xterm-viewport::-webkit-scrollbar) {
		width: 6px;
	}
	.terminal-host :global(.xterm-viewport::-webkit-scrollbar-thumb) {
		background: rgba(94, 140, 88, 0.18);
		border-radius: 3px;
	}
	.terminal-host :global(.xterm-viewport::-webkit-scrollbar-thumb:hover) {
		background: rgba(94, 140, 88, 0.32);
	}
</style>
