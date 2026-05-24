import { apiFetch, type ApiResult } from '$lib/api/client';

// Mirror of the backend state machine. Keep in sync with the `status` enum
// on the Capsule schema in internal/api/openapi.yaml.
export type CapsuleStatus =
	| 'pending'
	| 'starting'
	| 'running'
	| 'pausing'
	| 'paused'
	| 'snapshotting'
	| 'resuming'
	| 'stopping'
	| 'hibernated'
	| 'stopped'
	| 'missing'
	| 'error';

// States from which a user may resume the capsule.
export const RESUMABLE_STATUSES: ReadonlySet<CapsuleStatus> = new Set([
	'paused',
	'hibernated'
]);

// Transient states where lifecycle actions should be disabled.
export const TRANSIENT_STATUSES: ReadonlySet<CapsuleStatus> = new Set([
	'pending',
	'starting',
	'pausing',
	'snapshotting',
	'resuming',
	'stopping'
]);

export type Capsule = {
	id: string;
	status: CapsuleStatus;
	template: string;
	vcpus: number;
	memory_mb: number;
	timeout_sec: number;
	created_at: string;
	started_at?: string;
	last_active_at?: string;
	last_updated: string;
	metadata?: Record<string, string>;
	disk_size_mb: number;
	disk_used_mb?: number;
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
	/** True for built-in system base templates, which cannot be deleted. */
	protected?: boolean;
};

// Snapshots are async: the call returns 202 with the capsule now in the
// "snapshotting" state. The resulting template arrives later via the
// template.snapshot.create SSE event (or by polling listSnapshots).
export async function createSnapshot(capsuleId: string, name?: string): Promise<ApiResult<Capsule>> {
	return apiFetch('POST', '/api/v1/snapshots', { sandbox_id: capsuleId, name });
}

export async function listSnapshots(typeFilter?: string): Promise<ApiResult<Snapshot[]>> {
	const url = typeFilter ? `/api/v1/snapshots?type=${typeFilter}` : '/api/v1/snapshots';
	return apiFetch('GET', url);
}

export async function deleteSnapshot(name: string): Promise<ApiResult<void>> {
	return apiFetch('DELETE', `/api/v1/snapshots/${name}`);
}
