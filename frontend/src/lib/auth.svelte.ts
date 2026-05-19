import { goto } from '$app/navigation';

// Cookie-backed session auth. The browser holds the opaque session via the
// httpOnly `wrenn_sid` cookie (set by the server on login). JS never reads
// the session id; identity state is hydrated from GET /v1/me on app boot
// and after login/team-switch.

export type Me = {
	user_id: string;
	team_id: string;
	email: string;
	name: string;
	role: string;
	is_admin: boolean;
	has_password?: boolean;
	providers?: string[];
};

function createAuth() {
	let userId = $state<string | null>(null);
	let teamId = $state<string | null>(null);
	let email = $state<string | null>(null);
	let name = $state<string | null>(null);
	let isAdmin = $state(false);
	let role = $state<string>('member');
	let initialized = $state(false);

	function setUser(data: Me) {
		userId = data.user_id;
		teamId = data.team_id;
		email = data.email;
		name = data.name;
		role = data.role || 'member';
		isAdmin = Boolean(data.is_admin);
	}

	function clearUser() {
		userId = null;
		teamId = null;
		email = null;
		name = null;
		isAdmin = false;
		role = 'member';
	}

	async function init(): Promise<void> {
		if (typeof window === 'undefined') {
			initialized = true;
			return;
		}
		try {
			const res = await fetch('/api/v1/me', { credentials: 'same-origin' });
			if (res.ok) {
				const data = (await res.json()) as Me;
				setUser(data);
			} else {
				clearUser();
			}
		} catch {
			clearUser();
		}
		initialized = true;
	}

	async function logout(): Promise<void> {
		try {
			await fetch('/api/v1/auth/logout', {
				method: 'POST',
				credentials: 'same-origin',
				headers: { 'X-CSRF-Token': readCSRFToken() ?? '' }
			});
		} catch {
			/* best effort */
		}
		clearUser();
		await goto('/login');
	}

	return {
		get userId() {
			return userId;
		},
		get teamId() {
			return teamId;
		},
		get email() {
			return email;
		},
		get name() {
			return name;
		},
		get isAdmin() {
			return isAdmin;
		},
		get role() {
			return role;
		},
		get isAuthenticated() {
			return userId !== null;
		},
		get initialized() {
			return initialized;
		},

		setUser,
		clearUser,
		init,
		logout
	};
}

// readCSRFToken returns the value of the wrenn_csrf cookie, or null if absent.
// Exported so client.ts can attach the X-CSRF-Token header without duplicating
// the parser.
export function readCSRFToken(): string | null {
	if (typeof document === 'undefined') return null;
	const match = document.cookie.match(/(?:^|;\s*)wrenn_csrf=([^;]+)/);
	return match ? decodeURIComponent(match[1]) : null;
}

export const auth = createAuth();
