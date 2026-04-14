import { apiFetch, type ApiResult } from '$lib/api/client';

export type AdminUser = {
	id: string;
	email: string;
	name: string;
	is_admin: boolean;
	is_active: boolean;
	created_at: string;
	teams_joined: number;
	teams_owned: number;
};

export type AdminUsersResponse = {
	users: AdminUser[];
	total: number;
	page: number;
	per_page: number;
	total_pages: number;
};

export async function listAdminUsers(page: number = 1): Promise<ApiResult<AdminUsersResponse>> {
	return apiFetch('GET', `/api/v1/admin/users?page=${page}`);
}

export async function setUserActive(id: string, active: boolean): Promise<ApiResult<void>> {
	return apiFetch('PUT', `/api/v1/admin/users/${id}/active`, { active });
}
