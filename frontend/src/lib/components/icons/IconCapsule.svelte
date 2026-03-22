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
	<rect x="4" y="2" width="16" height="20" rx="4" />
	<path d="M4 12h16" />
	<circle cx="8" cy="7" r="1" fill="currentColor" stroke="none" />
	<circle cx="12" cy="7" r="1" fill="currentColor" stroke="none" />
</svg>
