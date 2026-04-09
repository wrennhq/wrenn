<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/stores';
	import { auth } from '$lib/auth.svelte';
	import { teams } from '$lib/teams.svelte';

	// Check for error in URL params (errors are still passed via query params).
	const params = $page.url.searchParams;
	const error = params.get('error');

	function getCookie(name: string): string | null {
		const match = document.cookie.match(new RegExp(`(?:^|; )${name}=([^;]*)`));
		return match ? decodeURIComponent(match[1]) : null;
	}

	function clearOAuthCookies() {
		for (const name of [
			'wrenn_oauth_token',
			'wrenn_oauth_user_id',
			'wrenn_oauth_team_id',
			'wrenn_oauth_email',
			'wrenn_oauth_name'
		]) {
			document.cookie = `${name}=; path=/auth/; max-age=0`;
		}
	}

	if (error) {
		goto(`/login?error=${encodeURIComponent(error)}`);
	} else {
		const token = getCookie('wrenn_oauth_token');
		const userId = getCookie('wrenn_oauth_user_id');
		const teamId = getCookie('wrenn_oauth_team_id');
		const email = getCookie('wrenn_oauth_email');
		const name = getCookie('wrenn_oauth_name') ?? '';

		clearOAuthCookies();

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
