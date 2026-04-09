import { apiFetch, type ApiResult } from '$lib/api/client';

export type AuditLog = {
	id: string;
	actor_type: 'user' | 'api_key' | 'system';
	actor_id?: string;
	actor_name?: string;
	resource_type: string;
	resource_id?: string;
	action: string;
	scope: 'team' | 'admin';
	status: 'success' | 'info' | 'warning' | 'error';
	metadata?: Record<string, unknown>;
	created_at: string;
};

export type AuditListResponse = {
	items: AuditLog[];
	next_before?: string;
	next_before_id?: string;
};

export async function listAuditLogs(params?: {
	before?: string;
	before_id?: string;
	resource_types?: string[];
	actions?: string[];
	limit?: number;
}): Promise<ApiResult<AuditListResponse>> {
	const q = new URLSearchParams();
	if (params?.before) q.set('before', params.before);
	if (params?.before_id) q.set('before_id', params.before_id);
	params?.resource_types?.forEach((t) => q.append('resource_type', t));
	params?.actions?.forEach((a) => q.append('action', a));
	if (params?.limit != null) q.set('limit', String(params.limit));
	const qs = q.toString();
	return apiFetch('GET', `/api/v1/audit-logs${qs ? '?' + qs : ''}`);
}
