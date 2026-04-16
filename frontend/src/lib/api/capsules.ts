import { apiFetch, type ApiResult } from '$lib/api/client';

export type Capsule = {
	id: string;
	status: string;
	template: string;
	vcpus: number;
	memory_mb: number;
	timeout_sec: number;
	guest_ip?: string;
	host_ip?: string;
	created_at: string;
	started_at?: string;
	last_active_at?: string;
	last_updated: string;
};


export async function listCapsules(): Promise<ApiResult<Capsule[]>> {
	return apiFetch('GET', '/api/v1/capsules');
}

export async function getCapsule(id: string): Promise<ApiResult<Capsule>> {
	return apiFetch('GET', `/api/v1/capsules/${id}`);
}

export type CreateCapsuleParams = {
	template?: string;
	vcpus?: number;
	memory_mb?: number;
	timeout_sec?: number;
};

export async function createCapsule(params: CreateCapsuleParams): Promise<ApiResult<Capsule>> {
	return apiFetch('POST', '/api/v1/capsules', params);
}

export async function pauseCapsule(id: string): Promise<ApiResult<Capsule>> {
	return apiFetch('POST', `/api/v1/capsules/${id}/pause`);
}

export async function resumeCapsule(id: string): Promise<ApiResult<Capsule>> {
	return apiFetch('POST', `/api/v1/capsules/${id}/resume`);
}

export async function destroyCapsule(id: string): Promise<ApiResult<void>> {
	return apiFetch('DELETE', `/api/v1/capsules/${id}`);
}

export type Snapshot = {
	name: string;
	type: string;
	vcpus?: number;
	memory_mb?: number;
	size_bytes: number;
	created_at: string;
	platform: boolean;
};

export async function createSnapshot(capsuleId: string, name?: string): Promise<ApiResult<Snapshot>> {
	return apiFetch('POST', '/api/v1/snapshots', { sandbox_id: capsuleId, name });
}

export async function listSnapshots(typeFilter?: string): Promise<ApiResult<Snapshot[]>> {
	const url = typeFilter ? `/api/v1/snapshots?type=${typeFilter}` : '/api/v1/snapshots';
	return apiFetch('GET', url);
}

export async function deleteSnapshot(name: string): Promise<ApiResult<void>> {
	return apiFetch('DELETE', `/api/v1/snapshots/${name}`);
}
