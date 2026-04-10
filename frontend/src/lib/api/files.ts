import { auth } from '$lib/auth.svelte';
import { type ApiResult } from '$lib/api/client';

export type FileEntry = {
	name: string;
	path: string;
	type: 'file' | 'directory' | 'symlink';
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

/**
 * Whether a file can be previewed as text in the browser.
 * Binary/unreadable extensions and files > 10 MB should be downloaded instead.
 */
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

export async function listDir(sandboxId: string, path: string, depth = 1): Promise<ApiResult<ListDirResponse>> {
	try {
		const headers: Record<string, string> = { 'Content-Type': 'application/json' };
		if (auth.token) headers['Authorization'] = `Bearer ${auth.token}`;

		const res = await fetch(`/api/v1/sandboxes/${sandboxId}/files/list`, {
			method: 'POST',
			headers,
			body: JSON.stringify({ path, depth }),
		});

		const data = await res.json();
		if (!res.ok) return { ok: false, error: data?.error?.message ?? 'Failed to list directory' };
		return { ok: true, data: data as ListDirResponse };
	} catch {
		return { ok: false, error: 'Unable to connect to the server' };
	}
}

export async function readFile(sandboxId: string, path: string): Promise<ApiResult<string>> {
	try {
		const headers: Record<string, string> = { 'Content-Type': 'application/json' };
		if (auth.token) headers['Authorization'] = `Bearer ${auth.token}`;

		const res = await fetch(`/api/v1/sandboxes/${sandboxId}/files/read`, {
			method: 'POST',
			headers,
			body: JSON.stringify({ path }),
		});

		if (!res.ok) {
			try {
				const data = await res.json();
				return { ok: false, error: data?.error?.message ?? 'Failed to read file' };
			} catch {
				return { ok: false, error: `HTTP ${res.status}` };
			}
		}

		const blob = await res.blob();
		const text = await blob.text();
		return { ok: true, data: text };
	} catch {
		return { ok: false, error: 'Unable to connect to the server' };
	}
}

export async function downloadFile(sandboxId: string, path: string, filename: string): Promise<void> {
	const headers: Record<string, string> = { 'Content-Type': 'application/json' };
	if (auth.token) headers['Authorization'] = `Bearer ${auth.token}`;

	const res = await fetch(`/api/v1/sandboxes/${sandboxId}/files/read`, {
		method: 'POST',
		headers,
		body: JSON.stringify({ path }),
	});

	if (!res.ok) throw new Error('Download failed');

	const blob = await res.blob();
	const url = URL.createObjectURL(blob);
	const a = document.createElement('a');
	a.href = url;
	a.download = filename;
	document.body.appendChild(a);
	a.click();
	a.remove();
	URL.revokeObjectURL(url);
}
