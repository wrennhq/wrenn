import { apiFetch, type ApiResult } from '$lib/api/client';

export type TeamMember = {
	user_id: string;
	name: string;
	email: string;
	role: 'owner' | 'admin' | 'member';
	joined_at: string;
};

export type TeamInfo = {
	id: string;
	name: string;
	slug: string;
	created_at: string;
};

export type TeamDetail = {
	team: TeamInfo;
	members: TeamMember[];
};

export type UserSearchResult = {
	user_id: string;
	email: string;
};

export type TeamWithRole = {
	id: string;
	name: string;
	slug: string;
	is_byoc: boolean;
	created_at: string;
	role: string;
};

export async function listTeams(): Promise<ApiResult<TeamWithRole[]>> {
	return apiFetch('GET', '/api/v1/teams');
}

export async function createTeam(name: string): Promise<ApiResult<TeamWithRole>> {
	return apiFetch('POST', '/api/v1/teams', { name });
}

export async function switchTeam(
	teamId: string
): Promise<ApiResult<{ token: string; user_id: string; team_id: string; email: string; name: string }>> {
	return apiFetch('POST', '/api/v1/auth/switch-team', { team_id: teamId });
}

export async function getTeam(id: string): Promise<ApiResult<TeamDetail>> {
	return apiFetch('GET', `/api/v1/teams/${id}`);
}

export async function updateTeam(id: string, name: string): Promise<ApiResult<void>> {
	return apiFetch('PATCH', `/api/v1/teams/${id}`, { name });
}

export async function addMember(id: string, email: string): Promise<ApiResult<TeamMember>> {
	return apiFetch('POST', `/api/v1/teams/${id}/members`, { email });
}

export async function removeMember(id: string, userId: string): Promise<ApiResult<void>> {
	return apiFetch('DELETE', `/api/v1/teams/${id}/members/${userId}`);
}

export async function updateMemberRole(
	id: string,
	userId: string,
	role: 'admin' | 'member'
): Promise<ApiResult<void>> {
	return apiFetch('PATCH', `/api/v1/teams/${id}/members/${userId}`, { role });
}

export async function deleteTeam(id: string): Promise<ApiResult<void>> {
	return apiFetch('DELETE', `/api/v1/teams/${id}`);
}

export async function leaveTeam(id: string): Promise<ApiResult<void>> {
	return apiFetch('POST', `/api/v1/teams/${id}/leave`);
}

export async function searchUsers(email: string): Promise<ApiResult<UserSearchResult[]>> {
	return apiFetch('GET', `/api/v1/users/search?email=${encodeURIComponent(email)}`);
}
