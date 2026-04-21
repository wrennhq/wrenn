import { apiFetch, type ApiResult } from '$lib/api/client';
import type { AuditLog, AuditListResponse } from '$lib/api/audit';

export type { AuditLog, AuditListResponse };

export async function listAdminAuditLogs(params?: {
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
	return apiFetch('GET', `/api/v1/admin/audit-logs${qs ? '?' + qs : ''}`);
}
