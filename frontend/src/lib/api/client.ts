import { auth } from '$lib/auth.svelte';

export type ApiResult<T> = { ok: true; data: T } | { ok: false; error: string };

async function parseResponse<T>(res: Response): Promise<ApiResult<T>> {
	if (res.status === 204 || res.status === 202) {
		const text = await res.text();
		if (!text) return { ok: true, data: undefined as T };
		const data = JSON.parse(text);
		return { ok: true, data: data as T };
	}

	const data = await res.json();
	if (!res.ok) return { ok: false, error: data?.error?.message ?? 'Something went wrong' };
	return { ok: true, data: data as T };
}

export async function apiFetch<T>(method: string, path: string, body?: unknown): Promise<ApiResult<T>> {
	try {
		const headers: Record<string, string> = { 'Content-Type': 'application/json' };
		if (auth.token) headers['Authorization'] = `Bearer ${auth.token}`;

		const res = await fetch(path, {
			method,
			headers,
			body: body ? JSON.stringify(body) : undefined
		});

		return await parseResponse<T>(res);
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

		return await parseResponse<T>(res);
	} catch {
		return { ok: false, error: 'Unable to connect to the server' };
	}
}
