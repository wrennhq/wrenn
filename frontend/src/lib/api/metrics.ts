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

export async function fetchSandboxMetrics(id: string, range: MetricRange): Promise<ApiResult<MetricsResponse>> {
	return apiFetch('GET', `/api/v1/sandboxes/${id}/metrics?range=${range}`);
}

export const METRIC_RANGES: MetricRange[] = ['5m', '10m', '1h', '6h', '24h'];

// All ranges poll every 10 seconds.
export const METRIC_POLL_INTERVAL = 10_000;
