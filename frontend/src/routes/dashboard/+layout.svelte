<script lang="ts">
	import Sidebar from '$lib/components/Sidebar.svelte';
	import Toaster from '$lib/components/Toaster.svelte';
	let { children } = $props();

	let collapsed = $state(
		typeof window !== 'undefined'
			? localStorage.getItem('wrenn_sidebar_collapsed') === 'true'
			: false
	);
</script>

<div class="flex h-screen overflow-hidden">
	<Sidebar bind:collapsed />
	<div class="flex flex-1 flex-col overflow-hidden">
		{@render children()}
	</div>
</div>
<Toaster />
