<script lang="ts">
	import '../app.css';
	import '$lib/auth.svelte';

	let { children } = $props();
</script>

{@render children()}
