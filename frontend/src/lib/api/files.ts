import { readCSRFToken } from '$lib/auth.svelte';
import { apiFailure, apiFetch, type ApiResult } from '$lib/api/client';

export type FileEntry = {
	name: string;
	path: string;
	type: 'file' | 'directory' | 'symlink' | 'unknown';
	size: number;
	mode: number;
	permissions: string;
	owner: string;
	group: string;
	modified_at: number;
	symlink_target?: string | null;
};

export type ListDirResponse = {
	entries: FileEntry[];
};

const MAX_READABLE_SIZE = 10 * 1024 * 1024; // 10 MB

const BINARY_EXTENSIONS = new Set([
	'.png', '.jpg', '.jpeg', '.gif', '.bmp', '.ico', '.webp', '.avif', '.svg',
	'.mp3', '.mp4', '.wav', '.ogg', '.flac', '.avi', '.mkv', '.mov', '.webm',
	'.zip', '.tar', '.gz', '.bz2', '.xz', '.7z', '.rar', '.zst',
	'.pdf', '.doc', '.docx', '.xls', '.xlsx', '.ppt', '.pptx',
	'.exe', '.dll', '.so', '.dylib', '.bin', '.o', '.a', '.class', '.pyc',
	'.woff', '.woff2', '.ttf', '.otf', '.eot',
	'.db', '.sqlite', '.sqlite3',
	'.iso', '.img', '.dmg',
]);

export function isBinaryFile(name: string): boolean {
	const dot = name.lastIndexOf('.');
	if (dot === -1) return false;
	return BINARY_EXTENSIONS.has(name.slice(dot).toLowerCase());
}

export function isFileTooLarge(size: number): boolean {
	return size > MAX_READABLE_SIZE;
}

export function formatFileSize(bytes: number): string {
	if (bytes === 0) return '0 B';
	const units = ['B', 'KB', 'MB', 'GB', 'TB'];
	const i = Math.floor(Math.log(bytes) / Math.log(1024));
	const val = bytes / Math.pow(1024, i);
	return `${val < 10 ? val.toFixed(1) : Math.round(val)} ${units[i]}`;
}

export function listDir(capsuleId: string, path: string, depth = 1, basePath = '/api/v1/capsules'): Promise<ApiResult<ListDirResponse>> {
	return apiFetch<ListDirResponse>('POST', `${basePath}/${capsuleId}/files/list`, { path, depth });
}

export async function readFile(
	capsuleId: string,
	path: string,
	signal?: AbortSignal,
	basePath = '/api/v1/capsules',
): Promise<ApiResult<string>> {
	// /files/read returns raw bytes (potentially binary) so we cannot route it
	// through apiFetch which assumes JSON. We still inject the CSRF token via
	// the shared cookie reader.
	try {
		const headers: Record<string, string> = { 'Content-Type': 'application/json' };
		const csrf = readCSRFToken();
		if (csrf) headers['X-CSRF-Token'] = csrf;

		const res = await fetch(`${basePath}/${capsuleId}/files/read`, {
			method: 'POST',
			headers,
			credentials: 'same-origin',
			body: JSON.stringify({ path }),
			signal,
		});

		if (!res.ok) {
			try {
				const data = await res.json();
				const failure = apiFailure(data, 'Failed to read file');
				return { ok: false, error: failure.error };
			} catch {
				return { ok: false, error: `HTTP ${res.status}` };
			}
		}

		const blob = await res.blob();
		const text = await blob.text();
		return { ok: true, data: text };
	} catch (e) {
		if (e instanceof DOMException && e.name === 'AbortError') {
			return { ok: false, error: 'Request aborted' };
		}
		return { ok: false, error: 'Unable to connect to the server' };
	}
}

export async function downloadFile(
	capsuleId: string,
	path: string,
	filename: string,
	signal?: AbortSignal,
	basePath = '/api/v1/capsules',
): Promise<void> {
	const headers: Record<string, string> = { 'Content-Type': 'application/json' };
	const csrf = readCSRFToken();
	if (csrf) headers['X-CSRF-Token'] = csrf;

	const res = await fetch(`${basePath}/${capsuleId}/files/read`, {
		method: 'POST',
		headers,
		credentials: 'same-origin',
		body: JSON.stringify({ path }),
		signal,
	});

	if (!res.ok) {
		let data: unknown;
		try {
			data = await res.json();
		} catch {
			throw new Error(`Download failed (HTTP ${res.status})`);
		}
		throw new Error(apiFailure(data, 'Download failed').error);
	}

	const blob = await res.blob();
	const url = URL.createObjectURL(blob);
	const a = document.createElement('a');
	a.href = url;
	a.download = filename;
	document.body.appendChild(a);
	a.click();
	a.remove();
	setTimeout(() => URL.revokeObjectURL(url), 5000);
}
