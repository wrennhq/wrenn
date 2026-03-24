import { browser } from '$app/environment';
import { redirect } from '@sveltejs/kit';
import { auth } from '$lib/auth.svelte';

export const load = () => {
	if (!browser) return;
	if (!auth.isAuthenticated) redirect(302, '/login');
	if (!auth.isAdmin) redirect(302, '/dashboard');
};
