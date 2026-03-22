import { apiFetch, type ApiResult } from '$lib/api/client';

export type APIKey = {
	id: string;
	team_id: string;
	name: string;
	key_prefix: string;
	created_by: string;
	creator_email?: string;
	created_at: string;
	last_used?: string;
	key?: string; // only present immediately after creation
};


export async function listKeys(): Promise<ApiResult<APIKey[]>> {
	return apiFetch('GET', '/api/v1/api-keys');
}

export async function createKey(name: string): Promise<ApiResult<APIKey>> {
	return apiFetch('POST', '/api/v1/api-keys', { name });
}

export async function revokeKey(id: string): Promise<ApiResult<void>> {
	return apiFetch('DELETE', `/api/v1/api-keys/${id}`);
}
