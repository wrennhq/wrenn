import { goto } from '$app/navigation';
import { auth, readCSRFToken } from '$lib/auth.svelte';

export type ApiResult<T> = { ok: true; data: T } | { ok: false; error: string };

async function parseResponse<T>(res: Response): Promise<ApiResult<T>> {
	if (res.status === 204 || res.status === 202) {
		const text = await res.text();
		if (!text) return { ok: true, data: undefined as T };
		const data = JSON.parse(text);
		return { ok: true, data: data as T };
	}

	if (res.status === 401) {
		auth.clearUser();
		if (typeof window !== 'undefined' && !window.location.pathname.startsWith('/login')) {
			goto('/login', { replaceState: true });
			return new Promise<ApiResult<T>>(() => {});
		}
	}

	const data = await res.json();
	if (!res.ok) return { ok: false, error: data?.error?.message ?? 'Something went wrong' };
	return { ok: true, data: data as T };
}

function attachCSRF(headers: Record<string, string>, method: string): void {
	if (method === 'GET' || method === 'HEAD' || method === 'OPTIONS') return;
	const token = readCSRFToken();
	if (token) headers['X-CSRF-Token'] = token;
}

export async function apiFetch<T>(method: string, path: string, body?: unknown): Promise<ApiResult<T>> {
	try {
		const headers: Record<string, string> = { 'Content-Type': 'application/json' };
		attachCSRF(headers, method);

		const res = await fetch(path, {
			method,
			headers,
			credentials: 'same-origin',
			body: body ? JSON.stringify(body) : undefined
		});

		return await parseResponse<T>(res);
	} catch {
		return { ok: false, error: 'Unable to connect to the server' };
	}
}

export async function apiFetchMultipart<T>(
	method: string,
	path: string,
	formData: FormData
): Promise<ApiResult<T>> {
	try {
		const headers: Record<string, string> = {};
		attachCSRF(headers, method);

		const res = await fetch(path, {
			method,
			headers,
			credentials: 'same-origin',
			body: formData
		});

		return await parseResponse<T>(res);
	} catch {
		return { ok: false, error: 'Unable to connect to the server' };
	}
}
