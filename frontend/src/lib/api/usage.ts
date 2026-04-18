import { apiFetch, type ApiResult } from '$lib/api/client';

export type UsagePoint = {
	date: string;
	cpu_minutes: number;
	ram_mb_minutes: number;
};

export type UsageResponse = {
	from: string;
	to: string;
	points: UsagePoint[];
};

export async function fetchUsage(from: string, to: string): Promise<ApiResult<UsageResponse>> {
	return apiFetch('GET', `/api/v1/capsules/usage?from=${from}&to=${to}`);
}

export function formatDate(d: Date): string {
	return d.toISOString().slice(0, 10);
}

export function defaultRange(): { from: string; to: string } {
	const to = new Date();
	const from = new Date(to);
	from.setDate(from.getDate() - 29);
	return { from: formatDate(from), to: formatDate(to) };
}
