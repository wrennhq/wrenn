import { apiFetch, type ApiResult } from '$lib/api/client';

export type BuildLogEntry = {
	step: number;
	cmd: string;
	stdout: string;
	stderr: string;
	exit: number;
	ok: boolean;
	elapsed_ms: number;
};

export type Build = {
	id: string;
	name: string;
	base_template: string;
	recipe: string[];
	healthcheck?: string;
	vcpus: number;
	memory_mb: number;
	status: string;
	current_step: number;
	total_steps: number;
	logs: BuildLogEntry[];
	error?: string;
	sandbox_id?: string;
	host_id?: string;
	created_at: string;
	started_at?: string;
	completed_at?: string;
};

export type CreateBuildParams = {
	name: string;
	base_template?: string;
	recipe: string[];
	healthcheck?: string;
	vcpus?: number;
	memory_mb?: number;
};

export async function createBuild(params: CreateBuildParams): Promise<ApiResult<Build>> {
	return apiFetch('POST', '/api/v1/admin/builds', params);
}

export async function listBuilds(): Promise<ApiResult<Build[]>> {
	return apiFetch('GET', '/api/v1/admin/builds');
}

export async function getBuild(id: string): Promise<ApiResult<Build>> {
	return apiFetch('GET', `/api/v1/admin/builds/${id}`);
}

export type AdminTemplate = {
	name: string;
	type: string;
	vcpus: number;
	memory_mb: number;
	size_bytes: number;
	team_id: string;
	created_at: string;
};

export async function listAdminTemplates(): Promise<ApiResult<AdminTemplate[]>> {
	return apiFetch('GET', '/api/v1/admin/templates');
}
