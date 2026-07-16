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
	/** True when the template is published and launchable by other teams. */
	public: boolean;
	/** True when the template belongs to the viewing team. */
	owned: boolean;
	/** Slug of the owning team. Foreign public templates launch as `<team_slug>/<name>`. */
	team_slug: string;
};

export type SnapshotPage = {
	templates: Snapshot[];
	total: number;
	page: number;
	per_page: number;
	total_pages: number;
};

export type ListSnapshotsParams = {
	type?: string;
	q?: string;
	page?: number;
	per_page?: number;
};

// Snapshots are async: the call returns 202 with the capsule now in the
// "snapshotting" state. The resulting template arrives later via the
// template.snapshot.create SSE event (or by polling listSnapshots).
export async function createSnapshot(capsuleId: string, name?: string): Promise<ApiResult<Capsule>> {
	return apiFetch('POST', '/api/v1/snapshots', { sandbox_id: capsuleId, name });
}

export async function listSnapshots(params: ListSnapshotsParams = {}): Promise<ApiResult<SnapshotPage>> {
	const q = new URLSearchParams();
	if (params.type) q.set('type', params.type);
	if (params.q) q.set('q', params.q);
	if (params.page) q.set('page', String(params.page));
	if (params.per_page) q.set('per_page', String(params.per_page));
	const qs = q.toString();
	return apiFetch('GET', qs ? `/api/v1/snapshots?${qs}` : '/api/v1/snapshots');
}

export async function deleteSnapshot(name: string): Promise<ApiResult<void>> {
	return apiFetch('DELETE', `/api/v1/snapshots/${encodeURIComponent(name)}`);
}

/**
 * Rename a template the team owns. Renaming unpublishes it, so any
 * `<team_slug>/<name>` references other teams held stop resolving.
 */
export async function renameTemplate(name: string, newName: string): Promise<ApiResult<void>> {
	return apiFetch('PATCH', `/api/v1/snapshots/${encodeURIComponent(name)}`, {
		new_name: newName
	});
}

/** Publish or unpublish a template the team owns. */
export async function setTemplateVisibility(
	name: string,
	isPublic: boolean
): Promise<ApiResult<void>> {
	return apiFetch('PATCH', `/api/v1/snapshots/${encodeURIComponent(name)}/visibility`, {
		public: isPublic
	});
}
