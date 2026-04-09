import { apiFetch, type ApiResult } from '$lib/api/client';

export type Channel = {
	id: string;
	team_id: string;
	name: string;
	provider: string;
	events: string[];
	created_at: string;
	updated_at: string;
	secret?: string; // only present immediately after creation (webhook provider)
};

export const PROVIDERS = [
	{ value: 'discord', label: 'Discord', fields: ['webhook_url'] },
	{ value: 'slack', label: 'Slack', fields: ['webhook_url'] },
	{ value: 'teams', label: 'Teams', fields: ['webhook_url'] },
	{ value: 'googlechat', label: 'Google Chat', fields: ['webhook_url'] },
	{ value: 'telegram', label: 'Telegram', fields: ['bot_token', 'chat_id'] },
	{ value: 'matrix', label: 'Matrix', fields: ['homeserver_url', 'access_token', 'room_id'] },
	{ value: 'webhook', label: 'Webhook', fields: ['url'] }
] as const;

export const EVENT_TYPES = [
	{ value: 'capsule.created', group: 'Capsule' },
	{ value: 'capsule.running', group: 'Capsule' },
	{ value: 'capsule.paused', group: 'Capsule' },
	{ value: 'capsule.destroyed', group: 'Capsule' },
	{ value: 'template.snapshot.created', group: 'Template' },
	{ value: 'template.snapshot.deleted', group: 'Template' },
	{ value: 'host.up', group: 'Host' },
	{ value: 'host.down', group: 'Host' }
] as const;

export async function listChannels(): Promise<ApiResult<Channel[]>> {
	return apiFetch('GET', '/api/v1/channels');
}

export async function createChannel(
	name: string,
	provider: string,
	config: Record<string, string>,
	events: string[]
): Promise<ApiResult<Channel>> {
	return apiFetch('POST', '/api/v1/channels', { name, provider, config, events });
}

export async function updateChannel(
	id: string,
	name: string,
	events: string[]
): Promise<ApiResult<Channel>> {
	return apiFetch('PATCH', `/api/v1/channels/${id}`, { name, events });
}

export async function deleteChannel(id: string): Promise<ApiResult<void>> {
	return apiFetch('DELETE', `/api/v1/channels/${id}`);
}

export async function rotateConfig(
	id: string,
	config: Record<string, string>
): Promise<ApiResult<Channel>> {
	return apiFetch('PUT', `/api/v1/channels/${id}/config`, { config });
}

export async function testChannel(
	provider: string,
	config: Record<string, string>
): Promise<ApiResult<{ status: string }>> {
	return apiFetch('POST', '/api/v1/channels/test', { provider, config });
}
