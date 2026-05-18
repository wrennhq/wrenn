<script lang="ts">
	import { onMount } from 'svelte';
	import Sidebar from '$lib/components/Sidebar.svelte';
	import Toaster from '$lib/components/Toaster.svelte';
	import { startSSE, stopSSE } from '$lib/sse.svelte';
	let { children } = $props();

	let collapsed = $state(
		typeof window !== 'undefined'
			? localStorage.getItem('wrenn_sidebar_collapsed') === 'true'
			: false
	);

	onMount(() => {
		startSSE();
		return () => stopSSE();
	});
</script>

<div class="flex h-screen overflow-hidden">
	<Sidebar bind:collapsed />
	<div class="flex flex-1 flex-col overflow-hidden">
		{@render children()}
	</div>
</div>
<Toaster />
