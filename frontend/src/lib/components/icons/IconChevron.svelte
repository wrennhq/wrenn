<script lang="ts">
	let {
		size = 18,
		direction = 'down',
		class: className = ''
	}: { size?: number; direction?: 'up' | 'down' | 'left' | 'right'; class?: string } = $props();

	const rotation = {
		up: '180',
		down: '0',
		left: '90',
		right: '-90'
	};
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
	style="transform: rotate({rotation[direction]}deg)"
	aria-hidden="true"
>
	<polyline points="6 9 12 15 18 9" />
</svg>
