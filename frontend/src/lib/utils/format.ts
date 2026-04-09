/**
 * Shared date/time formatting utilities.
 * All functions accept `string | undefined` and return a safe fallback.
 */

export function formatDate(iso: string | undefined): string {
	if (!iso) return '—';
	return new Date(iso).toLocaleString('en-US', {
		month: 'short',
		day: 'numeric',
		year: 'numeric',
		hour: '2-digit',
		minute: '2-digit',
		hour12: false
	});
}

export function timeAgo(iso: string | undefined): string {
	if (!iso) return '';
	const seconds = Math.floor((Date.now() - new Date(iso).getTime()) / 1000);
	if (seconds < 60) return `${seconds}s ago`;
	if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
	if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
	return `${Math.floor(seconds / 86400)}d ago`;
}
