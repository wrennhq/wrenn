import { redirect } from '@sveltejs/kit';
import { browser } from '$app/environment';
import { auth } from '$lib/auth.svelte';

export function load() {
	if (!browser) return;
	if (auth.isAuthenticated) {
		redirect(302, '/dashboard');
	}
	redirect(302, '/login');
}
