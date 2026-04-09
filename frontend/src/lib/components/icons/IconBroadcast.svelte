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
	<circle cx="12" cy="12" r="2" />
	<path d="M16.24 7.76a6 6 0 0 1 0 8.49" />
	<path d="M7.76 16.24a6 6 0 0 1 0-8.49" />
	<path d="M19.07 4.93a10 10 0 0 1 0 14.14" />
	<path d="M4.93 19.07a10 10 0 0 1 0-14.14" />
</svg>
