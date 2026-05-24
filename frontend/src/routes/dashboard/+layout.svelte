<script lang="ts">
	import { onMount } from 'svelte';
	import Sidebar from '$lib/components/Sidebar.svelte';
	import Toaster from '$lib/components/Toaster.svelte';
	import { startSSE, stopSSE, subscribeSSE } from '$lib/sse.svelte';
	import { lifecycleToast } from '$lib/lifecycle-toasts';
	let { children } = $props();

	let collapsed = $state(
		typeof window !== 'undefined'
			? localStorage.getItem('wrenn_sidebar_collapsed') === 'true'
			: false
	);

	onMount(() => {
		startSSE();
		// Lifecycle toasts live at the layout so they fire regardless of which
		// dashboard page is open (and survive navigation between them).
		const unsubscribe = subscribeSSE(lifecycleToast);
		return () => {
			unsubscribe();
			stopSSE();
		};
	});
</script>

<div class="flex h-screen overflow-hidden">
	<Sidebar bind:collapsed />
	<div class="flex flex-1 flex-col overflow-hidden">
		{@render children()}
	</div>
</div>
<Toaster />
