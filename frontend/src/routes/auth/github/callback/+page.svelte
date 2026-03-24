<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/stores';
	import { auth } from '$lib/auth.svelte';
	import { teams } from '$lib/teams.svelte';

	const params = $page.url.searchParams;
	const error = params.get('error');

	if (error) {
		goto(`/login?error=${encodeURIComponent(error)}`);
	} else {
		const token = params.get('token');
		const userId = params.get('user_id');
		const teamId = params.get('team_id');
		const email = params.get('email');
		const name = params.get('name') ?? '';

		if (token && userId && teamId && email) {
			teams.reset();
			auth.login({ token, user_id: userId, team_id: teamId, email, name });
			goto('/dashboard');
		} else {
			goto('/login?error=missing_token');
		}
	}
</script>

<div class="flex min-h-screen items-center justify-center">
	<p class="text-ui text-[var(--color-text-secondary)]">Signing you in...</p>
</div>
