import { apiFetch, apiFetchMultipart, type ApiResult } from '$lib/api/client';

export type BuildLogEntry = {
	step: number;
	phase: string; // "pre-build", "recipe", "post-build", or "healthcheck"
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
	default_user: string;
	default_env: Record<string, string>;
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
	skip_pre_post?: boolean;
	run_as_root?: boolean;
	archive?: File;
};

// BuildStreamEvent is one message from the live build console WebSocket
// (GET /v1/admin/builds/{id}/stream). It mirrors the backend event shape.
export type BuildStreamEvent = {
	type: 'step-start' | 'output' | 'step-end' | 'build-status' | 'ping';
	step?: number;
	phase?: string;
	cmd?: string;
	data?: string; // base64-encoded PTY output bytes
	exit?: number;
	ok?: boolean;
	elapsed_ms?: number;
	status?: string;
	current_step?: number;
	total_steps?: number;
	error?: string;
	t?: number;
};

// buildStreamUrl returns the WebSocket URL for a build's live console.
export function buildStreamUrl(id: string): string {
	const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
	return `${proto}//${window.location.host}/api/v1/admin/builds/${id}/stream`;
}

export async function createBuild(params: CreateBuildParams): Promise<ApiResult<Build>> {
	if (params.archive) {
		// Use multipart when an archive file is provided.
		const { archive, ...config } = params;
		const formData = new FormData();
		formData.append('config', JSON.stringify(config));
		formData.append('archive', archive);
		return apiFetchMultipart('POST', '/api/v1/admin/builds', formData);
	}
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
	/** True for built-in system base templates, which cannot be deleted. */
	protected: boolean;
};

export async function listAdminTemplates(): Promise<ApiResult<AdminTemplate[]>> {
	return apiFetch('GET', '/api/v1/admin/templates');
}

export async function deleteAdminTemplate(name: string): Promise<ApiResult<void>> {
	return apiFetch('DELETE', `/api/v1/admin/templates/${name}`);
}

export async function cancelBuild(id: string): Promise<ApiResult<void>> {
	return apiFetch('POST', `/api/v1/admin/builds/${id}/cancel`);
}
