import { apiFetch, type ApiResult } from '$lib/api/client';

export type MetricRange = '5m' | '10m' | '1h' | '6h' | '24h';

export type MetricPoint = {
	timestamp_unix: number;
	cpu_pct: number;
	mem_bytes: number;
	disk_bytes: number;
};

export type MetricsResponse = {
	sandbox_id: string;
	range: MetricRange;
	points: MetricPoint[];
};

export async function fetchCapsuleMetrics(id: string, range: MetricRange, basePath = '/api/v1/capsules'): Promise<ApiResult<MetricsResponse>> {
	return apiFetch('GET', `${basePath}/${id}/metrics?range=${range}`);
}

export const METRIC_RANGES: MetricRange[] = ['5m', '10m', '1h', '6h', '24h'];

// Poll interval varies by range — shorter ranges need fresher data.
export const METRIC_POLL_INTERVALS: Record<MetricRange, number> = {
	'5m':  10_000,
	'10m': 10_000,
	'1h':  30_000,
	'6h':  60_000,
	'24h': 120_000,
};

/** @deprecated Use METRIC_POLL_INTERVALS instead */
export const METRIC_POLL_INTERVAL = 10_000;
