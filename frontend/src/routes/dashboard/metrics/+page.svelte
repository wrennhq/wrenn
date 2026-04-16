<script lang="ts">
	import StatsPanel from '$lib/components/StatsPanel.svelte';
	import CreateCapsuleDialog from '$lib/components/CreateCapsuleDialog.svelte';
	import { auth } from '$lib/auth.svelte';

	let showCreateDialog = $state(false);
</script>

<svelte:head>
	<title>Wrenn — Metrics</title>
</svelte:head>

	<main class="flex-1 overflow-y-auto bg-[var(--color-bg-0)]">
			<div class="px-7 pt-8">
				<h1 class="font-serif text-page text-[var(--color-text-bright)]">
					Metrics
				</h1>
				<p class="mt-2 text-ui text-[var(--color-text-secondary)]">
					Resource usage and performance across all capsules.
				</p>

				<div class="mt-6 border-b border-[var(--color-border)]"></div>
			</div>

			<StatsPanel
				onlaunch={() => { showCreateDialog = true; }}
				launchDisabled={!auth.teamId}
			/>
		</main>

		<footer
			class="flex h-7 shrink-0 items-center justify-end border-t border-[var(--color-border)] bg-[var(--color-bg-1)] px-7"
		>
			<div class="flex items-center gap-1.5">
				<span class="relative flex h-[5px] w-[5px]">
					<span class="animate-status-ping absolute inline-flex h-full w-full rounded-full bg-[var(--color-accent)]"></span>
					<span class="relative inline-flex h-[5px] w-[5px] rounded-full bg-[var(--color-accent)]"></span>
				</span>
				<span class="font-mono text-label uppercase tracking-[0.04em] text-[var(--color-text-secondary)]">All systems operational</span>
			</div>
		</footer>

<CreateCapsuleDialog
	open={showCreateDialog}
	onclose={() => { showCreateDialog = false; }}
/>
