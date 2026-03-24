export type AuthResponse = {
	token: string;
	user_id: string;
	team_id: string;
	email: string;
	name: string;
};

export type AuthResult = { ok: true; data: AuthResponse } | { ok: false; error: string };

export async function apiLogin(email: string, password: string): Promise<AuthResult> {
	return authFetch('/api/v1/auth/login', { email, password });
}

export async function apiSignup(email: string, password: string, name: string): Promise<AuthResult> {
	return authFetch('/api/v1/auth/signup', { email, password, name });
}

async function authFetch(url: string, body: Record<string, string>): Promise<AuthResult> {
	try {
		const res = await fetch(url, {
			method: 'POST',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify(body)
		});

		const data = await res.json();

		if (!res.ok) {
			const message = data?.error?.message ?? 'Something went wrong';
			return { ok: false, error: message };
		}

		return { ok: true, data: data as AuthResponse };
	} catch {
		return { ok: false, error: 'Unable to connect to the server' };
	}
}
