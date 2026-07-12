import { goto } from '$app/navigation';
import { auth, readCSRFToken } from '$lib/auth.svelte';

export type ApiFailure = {
	ok: false;
	/** Human-readable message, ready to show in a toast. */
	error: string;
	/** Machine-readable error code, e.g. "sandbox_not_running". */
	code?: string;
	/** Server request ID — quote it when reporting issues. */
	requestId?: string;
	/** Whether retrying the same request may succeed. */
	retryable?: boolean;
	details?: Record<string, unknown>;
};

export type ApiResult<T> = { ok: true; data: T } | ApiFailure;

/**
 * Normalizes the API error envelope
 * `{"error":{code,message,request_id,retryable,details}}` into an ApiFailure.
 * Internal errors get the request ID appended so users can quote it.
 */
export function apiFailure(data: unknown, fallback = 'Something went wrong'): ApiFailure {
	const e = (data as { error?: Record<string, unknown> } | null)?.error;
	let message = typeof e?.message === 'string' && e.message ? e.message : fallback;
	const requestId = typeof e?.request_id === 'string' ? e.request_id : undefined;
	if (requestId && e?.code === 'internal_error') {
		message += ` (ref: ${requestId})`;
	}
	return {
		ok: false,
		error: message,
		code: typeof e?.code === 'string' ? e.code : undefined,
		requestId,
		retryable: e?.retryable === true,
		details: (e?.details as Record<string, unknown> | undefined) ?? undefined
	};
}

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

	let data: unknown;
	try {
		data = await res.json();
	} catch {
		// Non-JSON body (e.g. a 502/504 from the proxy when the control plane
		// is unreachable). Surface the status rather than masking it as a
		// connection failure in the caller's catch.
		if (!res.ok) return { ok: false, error: `HTTP ${res.status}` };
		return { ok: true, data: undefined as T };
	}
	if (!res.ok) return apiFailure(data);
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
