import { auth } from '$lib/auth.svelte';

export type ApiResult<T> = { ok: true; data: T } | { ok: false; error: string };

export async function apiFetch<T>(method: string, path: string, body?: unknown): Promise<ApiResult<T>> {
	try {
		const headers: Record<string, string> = { 'Content-Type': 'application/json' };
		if (auth.token) headers['Authorization'] = `Bearer ${auth.token}`;

		const res = await fetch(path, {
			method,
			headers,
			body: body ? JSON.stringify(body) : undefined
		});

		if (res.status === 204) return { ok: true, data: undefined as T };

		const data = await res.json();
		if (!res.ok) return { ok: false, error: data?.error?.message ?? 'Something went wrong' };
		return { ok: true, data: data as T };
	} catch {
		return { ok: false, error: 'Unable to connect to the server' };
	}
}

export async function apiFetchMultipart<T>(method: string, path: string, formData: FormData): Promise<ApiResult<T>> {
	try {
		const headers: Record<string, string> = {};
		if (auth.token) headers['Authorization'] = `Bearer ${auth.token}`;

		const res = await fetch(path, {
			method,
			headers,
			body: formData
		});

		if (res.status === 204) return { ok: true, data: undefined as T };

		const data = await res.json();
		if (!res.ok) return { ok: false, error: data?.error?.message ?? 'Something went wrong' };
		return { ok: true, data: data as T };
	} catch {
		return { ok: false, error: 'Unable to connect to the server' };
	}
}
