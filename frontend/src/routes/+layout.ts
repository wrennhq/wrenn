// Static site generation — all pages prerendered.
import { browser } from '$app/environment';
import { auth } from '$lib/auth.svelte';

export const prerender = true;
export const ssr = false;

// Bootstrap auth state once for the whole app. Children load functions can
// then read auth.isAuthenticated synchronously without an async race.
export async function load() {
	if (!browser) return;
	if (!auth.initialized) {
		await auth.init();
	}
}
