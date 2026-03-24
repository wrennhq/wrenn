import { apiFetch } from './client';

export type Host = {
	id: string;
	type: 'regular' | 'byoc';
	team_id?: string;
	team_name?: string;
	provider?: string;
	availability_zone?: string;
	arch?: string;
	cpu_cores?: number;
	memory_mb?: number;
	disk_gb?: number;
	address?: string;
	status: 'pending' | 'online' | 'offline' | 'unreachable' | 'draining';
	last_heartbeat_at?: string;
	created_by: string;
	created_at: string;
	updated_at: string;
};

export type CreateHostParams = {
	type: 'regular' | 'byoc';
	team_id?: string;
	provider?: string;
	availability_zone?: string;
};

export type CreateHostResult = {
	host: Host;
	registration_token: string;
};

export async function listHosts(): Promise<{ ok: true; data: Host[] } | { ok: false; error: string }> {
	return apiFetch<Host[]>('GET', '/api/v1/hosts');
}

export async function createHost(
	params: CreateHostParams
): Promise<{ ok: true; data: CreateHostResult } | { ok: false; error: string }> {
	return apiFetch<CreateHostResult>('POST', '/api/v1/hosts', params);
}

export async function deleteHost(
	id: string,
	force = false
): Promise<{ ok: true } | { ok: false; error: string; sandbox_ids?: string[] }> {
	const url = `/api/v1/hosts/${id}${force ? '?force=true' : ''}`;
	const res = await apiFetch<void>('DELETE', url);
	if (!res.ok) {
		return res as { ok: false; error: string };
	}
	return { ok: true };
}

export async function getDeletePreview(
	id: string
): Promise<{ ok: true; data: { host: Host; sandbox_ids: string[] } } | { ok: false; error: string }> {
	return apiFetch<{ host: Host; sandbox_ids: string[] }>('GET', `/api/v1/hosts/${id}/delete-preview`);
}

export function statusColor(status: Host['status']): string {
	switch (status) {
		case 'online':
			return 'var(--color-accent)';
		case 'pending':
			return 'var(--color-amber)';
		case 'offline':
		case 'unreachable':
			return 'var(--color-red)';
		case 'draining':
			return 'var(--color-blue)';
		default:
			return 'var(--color-text-muted)';
	}
}

export function formatSpecs(host: Host): string {
	const parts: string[] = [];
	if (host.cpu_cores) parts.push(`${host.cpu_cores} vCPU`);
	if (host.memory_mb) parts.push(`${Math.round(host.memory_mb / 1024)}GB RAM`);
	if (host.disk_gb) parts.push(`${host.disk_gb}GB disk`);
	return parts.join(' · ') || '—';
}
