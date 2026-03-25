<script lang="ts">
	import StatsPanel from '$lib/components/StatsPanel.svelte';
	import CreateCapsuleDialog from '$lib/components/CreateCapsuleDialog.svelte';
	import { auth } from '$lib/auth.svelte';

	let showCreateDialog = $state(false);
</script>

<StatsPanel
	onlaunch={() => { showCreateDialog = true; }}
	launchDisabled={!auth.teamId}
/>

<CreateCapsuleDialog
	open={showCreateDialog}
	onclose={() => { showCreateDialog = false; }}
/>
