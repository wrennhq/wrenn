import { goto } from '$app/navigation';

const STORAGE_KEYS = {
	token: 'wrenn_token',
	userId: 'wrenn_user_id',
	teamId: 'wrenn_team_id',
	email: 'wrenn_email'
} as const;

function isTokenExpired(token: string): boolean {
	try {
		const payload = token.split('.')[1];
		const decoded = atob(payload.replace(/-/g, '+').replace(/_/g, '/'));
		const { exp } = JSON.parse(decoded);
		return Date.now() / 1000 >= exp;
	} catch {
		return true;
	}
}

function createAuth() {
	let token = $state<string | null>(null);
	let userId = $state<string | null>(null);
	let teamId = $state<string | null>(null);
	let email = $state<string | null>(null);
	let initialized = $state(false);

	// Initialize from localStorage synchronously at module load.
	if (typeof window !== 'undefined') {
		const stored = localStorage.getItem(STORAGE_KEYS.token);
		if (stored && !isTokenExpired(stored)) {
			token = stored;
			userId = localStorage.getItem(STORAGE_KEYS.userId);
			teamId = localStorage.getItem(STORAGE_KEYS.teamId);
			email = localStorage.getItem(STORAGE_KEYS.email);
		} else if (stored) {
			// Expired — clean up.
			for (const key of Object.values(STORAGE_KEYS)) {
				localStorage.removeItem(key);
			}
		}
		initialized = true;
	}

	const isAuthenticated = $derived(token !== null && !isTokenExpired(token));

	return {
		get token() {
			return token;
		},
		get userId() {
			return userId;
		},
		get teamId() {
			return teamId;
		},
		get email() {
			return email;
		},
		get isAuthenticated() {
			return isAuthenticated;
		},
		get initialized() {
			return initialized;
		},

		login(data: { token: string; user_id: string; team_id: string; email: string }) {
			token = data.token;
			userId = data.user_id;
			teamId = data.team_id;
			email = data.email;

			localStorage.setItem(STORAGE_KEYS.token, data.token);
			localStorage.setItem(STORAGE_KEYS.userId, data.user_id);
			localStorage.setItem(STORAGE_KEYS.teamId, data.team_id);
			localStorage.setItem(STORAGE_KEYS.email, data.email);
		},

		logout() {
			token = null;
			userId = null;
			teamId = null;
			email = null;

			for (const key of Object.values(STORAGE_KEYS)) {
				localStorage.removeItem(key);
			}

			goto('/login');
		}
	};
}

export const auth = createAuth();
