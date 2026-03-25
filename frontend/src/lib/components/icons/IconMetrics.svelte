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
	<line x1="18" y1="20" x2="18" y2="10" />
	<line x1="12" y1="20" x2="12" y2="4" />
	<line x1="6" y1="20" x2="6" y2="14" />
</svg>
