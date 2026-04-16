<script lang="ts">
	import AdminSidebar from '$lib/components/AdminSidebar.svelte';
	import Toaster from '$lib/components/Toaster.svelte';
	let { children } = $props();

	let collapsed = $state(
		typeof window !== 'undefined'
			? localStorage.getItem('wrenn_sidebar_collapsed') === 'true'
			: false
	);
</script>

<div class="flex h-screen overflow-hidden">
	<AdminSidebar bind:collapsed />
	<div class="flex flex-1 flex-col overflow-hidden">
		{@render children()}
	</div>
</div>
<Toaster />
