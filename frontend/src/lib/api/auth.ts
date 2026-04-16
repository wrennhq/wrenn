export type AuthResponse = {
	token: string;
	user_id: string;
	team_id: string;
	email: string;
	name: string;
};

export type SignupResponse = {
	message: string;
};

export type AuthResult = { ok: true; data: AuthResponse } | { ok: false; error: string };
export type SignupResult = { ok: true; data: SignupResponse } | { ok: false; error: string };

export async function apiLogin(email: string, password: string): Promise<AuthResult> {
	return authFetch('/api/v1/auth/login', { email, password });
}

export async function apiSignup(email: string, password: string, name: string): Promise<SignupResult> {
	return authFetch('/api/v1/auth/signup', { email, password, name });
}

export async function apiActivate(token: string): Promise<AuthResult> {
	return authFetch('/api/v1/auth/activate', { token });
}

async function authFetch<T = AuthResponse>(url: string, body: Record<string, string>): Promise<{ ok: true; data: T } | { ok: false; error: string }> {
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

		return { ok: true, data: data as T };
	} catch {
		return { ok: false, error: 'Unable to connect to the server' };
	}
}
