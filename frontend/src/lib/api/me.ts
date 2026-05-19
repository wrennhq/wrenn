import { apiFetch, type ApiResult } from '$lib/api/client';

export type MeResponse = {
	user_id: string;
	team_id: string;
	name: string;
	email: string;
	role: string;
	is_admin: boolean;
	has_password: boolean;
	providers: string[];
};

export type SessionRow = {
	id: string;
	user_agent: string;
	ip_address: string;
	created_at: string;
	last_seen_at: string;
	expires_at: string;
	current: boolean;
};

export type ChangePasswordBody = {
	current_password?: string;
	new_password: string;
	confirm_password?: string;
};

export const getMe = (): Promise<ApiResult<MeResponse>> =>
	apiFetch('GET', '/api/v1/me');

export const updateName = (name: string): Promise<ApiResult<void>> =>
	apiFetch('PATCH', '/api/v1/me', { name });

export const changePassword = (body: ChangePasswordBody): Promise<ApiResult<void>> =>
	apiFetch('POST', '/api/v1/me/password', body);

export const requestPasswordReset = (email: string): Promise<ApiResult<void>> =>
	apiFetch('POST', '/api/v1/me/password/reset', { email });

export const confirmPasswordReset = (
	token: string,
	new_password: string
): Promise<ApiResult<void>> =>
	apiFetch('POST', '/api/v1/me/password/reset/confirm', { token, new_password });

export const getProviderConnectURL = (provider: string): Promise<ApiResult<{ auth_url: string }>> =>
	apiFetch('GET', `/api/v1/me/providers/${provider}/connect`);

export const disconnectProvider = (provider: string): Promise<ApiResult<void>> =>
	apiFetch('DELETE', `/api/v1/me/providers/${provider}`);

export const deleteAccount = (confirmation: string): Promise<ApiResult<void>> =>
	apiFetch('DELETE', '/api/v1/me', { confirmation });

export const listSessions = (): Promise<ApiResult<{ sessions: SessionRow[] }>> =>
	apiFetch('GET', '/api/v1/me/sessions');

export const revokeSession = (id: string): Promise<ApiResult<void>> =>
	apiFetch('DELETE', `/api/v1/me/sessions/${id}`);

export const logout = (): Promise<ApiResult<void>> =>
	apiFetch('POST', '/api/v1/auth/logout');

export const logoutAll = (): Promise<ApiResult<void>> =>
	apiFetch('POST', '/api/v1/auth/logout-all');
