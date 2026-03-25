import { apiFetch, type ApiResult } from '$lib/api/client';

export type TimeRange = '5m' | '1h' | '6h' | '24h' | '30d';

export type StatsResponse = {
	range: TimeRange;
	current: {
		running_count: number;
		vcpus_reserved: number;
		memory_mb_reserved: number;
		sampled_at?: string;
	};
	peaks: {
		running_count: number;
		vcpus: number;
		memory_mb: number;
	};
	series: {
		labels: string[];
		running: number[];
		vcpus: number[];
		memory_mb: number[];
	};
};

export async function fetchStats(range: TimeRange): Promise<ApiResult<StatsResponse>> {
	return apiFetch('GET', `/api/v1/sandboxes/stats?range=${range}`);
}

export const POLL_INTERVALS: Record<TimeRange, number> = {
	'5m':  15_000,
	'1h':  30_000,
	'6h':  60_000,
	'24h': 120_000,
	'30d': 300_000,
};

export const RANGE_LABELS: Record<TimeRange, string> = {
	'5m':  '5m',
	'1h':  '1h',
	'6h':  '6h',
	'24h': '24h',
	'30d': '30d',
};
