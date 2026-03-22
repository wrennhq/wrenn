<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/stores';
	import { auth } from '$lib/auth.svelte';

	const params = $page.url.searchParams;
	const error = params.get('error');

	if (error) {
		goto(`/login?error=${encodeURIComponent(error)}`);
	} else {
		const token = params.get('token');
		const userId = params.get('user_id');
		const teamId = params.get('team_id');
		const email = params.get('email');

		if (token && userId && teamId && email) {
			auth.login({ token, user_id: userId, team_id: teamId, email });
			goto('/dashboard');
		} else {
			goto('/login?error=missing_token');
		}
	}
</script>

<div class="flex min-h-screen items-center justify-center">
	<p class="text-[13px] text-[var(--color-text-secondary)]">Signing you in...</p>
</div>
