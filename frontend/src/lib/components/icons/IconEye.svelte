<script lang="ts">
	let { size = 18, class: className = '' }: { size?: number; class?: string } = $props();
</script>

<svg
	width={size}
	height={size}
	viewBox="0 0 24 24"
	fill="none"
	stroke="currentColor"
	stroke-width="1.75"
	stroke-linecap="round"
	stroke-linejoin="round"
	class={className}
	aria-hidden="true"
>
	<path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z" />
	<circle cx="12" cy="12" r="3" />
</svg>
