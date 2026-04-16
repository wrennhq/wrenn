import { apiFetch, type ApiResult } from '$lib/api/client';
import type { Capsule, CreateCapsuleParams, Snapshot } from '$lib/api/capsules';
import type { AdminTemplate } from '$lib/api/builds';

export async function listAdminCapsules(): Promise<ApiResult<Capsule[]>> {
	return apiFetch('GET', '/api/v1/admin/capsules');
}

export async function getAdminCapsule(id: string): Promise<ApiResult<Capsule>> {
	return apiFetch('GET', `/api/v1/admin/capsules/${id}`);
}

export async function createAdminCapsule(params: CreateCapsuleParams): Promise<ApiResult<Capsule>> {
	return apiFetch('POST', '/api/v1/admin/capsules', params);
}

export async function destroyAdminCapsule(id: string): Promise<ApiResult<void>> {
	return apiFetch('DELETE', `/api/v1/admin/capsules/${id}`);
}

export async function snapshotAdminCapsule(id: string, name?: string): Promise<ApiResult<Snapshot>> {
	return apiFetch('POST', `/api/v1/admin/capsules/${id}/snapshot`, { name });
}

/** Fetch platform templates for the admin create dialog. */
export async function listPlatformTemplates(): Promise<ApiResult<Snapshot[]>> {
	const result = await apiFetch<AdminTemplate[]>('GET', '/api/v1/admin/templates');
	if (!result.ok) return result;
	// Map AdminTemplate → Snapshot shape.
	const snapshots: Snapshot[] = result.data.map((t) => ({
		name: t.name,
		type: t.type,
		vcpus: t.vcpus || undefined,
		memory_mb: t.memory_mb || undefined,
		size_bytes: t.size_bytes,
		created_at: t.created_at,
		platform: true,
	}));
	return { ok: true, data: snapshots };
}
