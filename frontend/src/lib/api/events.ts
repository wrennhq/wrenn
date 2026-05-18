import { auth } from '$lib/auth.svelte';
import type { Capsule } from '$lib/api/capsules';

// Mirror the SSE event names emitted by pkg/events. Keep in sync with the
// `SSEEvent.event` enum in internal/api/openapi.yaml.
export type SSEEventKind =
	| 'connected'
	| 'capsule.created'
	| 'capsule.running'
	| 'capsule.paused'
	| 'capsule.destroyed'
	| 'capsule.error'
	| 'template.snapshot.created'
	| 'template.snapshot.deleted'
	| 'host.up'
	| 'host.down';

export type SSEEvent = {
	event: SSEEventKind;
	timestamp: string;
	team_id: string;
	actor: { type: string; id?: string; name?: string };
	resource: { id: string; type: string };
	sandbox?: Capsule | null;
};

// Narrow type guard so malformed wire payloads can't reach handlers via
// blind casts. We accept anything with the structural minimum required for
// handlers to operate; richer validation lives in the handlers themselves.
function isSSEEvent(x: unknown): x is SSEEvent {
	if (!x || typeof x !== 'object') return false;
	const o = x as Record<string, unknown>;
	return (
		typeof o.event === 'string' &&
		typeof o.resource === 'object' &&
		o.resource !== null
	);
}

export type SSEEventHandler = (event: SSEEvent) => void;

export type EventStreamConnection = {
	close: () => void;
};

async function fetchTicket(admin: boolean): Promise<string | null> {
	const token = auth.token;
	if (!token) return null;

	const path = admin ? '/api/v1/admin/events/token' : '/api/v1/events/token';
	try {
		const res = await fetch(path, {
			method: 'POST',
			headers: {
				'Content-Type': 'application/json',
				Authorization: `Bearer ${token}`
			}
		});
		if (!res.ok) return null;
		const data = await res.json();
		return data.ticket ?? null;
	} catch {
		return null;
	}
}

/**
 * Connects to the SSE event stream. Returns a handle to close the connection.
 * Automatically reconnects on disconnect with exponential backoff.
 * Uses a short-lived ticket (obtained via POST) to avoid exposing JWTs in URLs.
 */
export function connectEventStream(
	onEvent: SSEEventHandler,
	opts?: { admin?: boolean }
): EventStreamConnection {
	let closed = false;
	let eventSource: EventSource | null = null;
	let reconnectTimeout: ReturnType<typeof setTimeout> | null = null;
	let backoff = 1000;

	function scheduleReconnect() {
		if (closed) return;
		if (reconnectTimeout) return;
		reconnectTimeout = setTimeout(() => {
			reconnectTimeout = null;
			connect();
		}, backoff);
		backoff = Math.min(backoff * 2, 30000);
	}

	function reconnectNow() {
		if (closed) return;
		if (reconnectTimeout) {
			clearTimeout(reconnectTimeout);
			reconnectTimeout = null;
		}
		eventSource?.close();
		eventSource = null;
		backoff = 1000;
		connect();
	}

	async function connect() {
		if (closed) return;

		const isAdmin = opts?.admin ?? false;
		const ticket = await fetchTicket(isAdmin);
		if (closed) return;
		if (!ticket) {
			// Ticket fetch failed (401, network blip, etc.). Retry with backoff
			// instead of giving up — token may come back, network may recover.
			scheduleReconnect();
			return;
		}

		const basePath = isAdmin ? '/api/v1/admin/events/stream' : '/api/v1/events/stream';
		const url = `${basePath}?ticket=${encodeURIComponent(ticket)}`;

		eventSource = new EventSource(url);

		eventSource.onopen = () => {
			backoff = 1000;
			if (reconnectTimeout) {
				clearTimeout(reconnectTimeout);
				reconnectTimeout = null;
			}
		};

		eventSource.onerror = () => {
			eventSource?.close();
			eventSource = null;
			scheduleReconnect();
		};

		eventSource.addEventListener('capsule.created', handleEvent);
		eventSource.addEventListener('capsule.running', handleEvent);
		eventSource.addEventListener('capsule.paused', handleEvent);
		eventSource.addEventListener('capsule.destroyed', handleEvent);
		eventSource.addEventListener('capsule.error', handleEvent);
		eventSource.addEventListener('template.snapshot.created', handleEvent);
		eventSource.addEventListener('template.snapshot.deleted', handleEvent);
		eventSource.addEventListener('host.up', handleEvent);
		eventSource.addEventListener('host.down', handleEvent);
	}

	function handleEvent(e: MessageEvent) {
		try {
			const parsed = JSON.parse(e.data);
			if (!isSSEEvent(parsed)) {
				console.warn('SSE event failed shape validation, dropping', parsed);
				return;
			}
			onEvent(parsed);
		} catch {
			// Ignore malformed messages.
		}
	}

	function isDisconnected() {
		return !eventSource || eventSource.readyState !== EventSource.OPEN;
	}

	function handleOnline() {
		if (isDisconnected()) reconnectNow();
	}

	function handleVisibility() {
		if (typeof document !== 'undefined' && document.visibilityState === 'visible' && isDisconnected()) {
			reconnectNow();
		}
	}

	if (typeof window !== 'undefined') {
		window.addEventListener('online', handleOnline);
	}
	if (typeof document !== 'undefined') {
		document.addEventListener('visibilitychange', handleVisibility);
	}

	function close() {
		closed = true;
		eventSource?.close();
		eventSource = null;
		if (reconnectTimeout) {
			clearTimeout(reconnectTimeout);
			reconnectTimeout = null;
		}
		if (typeof window !== 'undefined') {
			window.removeEventListener('online', handleOnline);
		}
		if (typeof document !== 'undefined') {
			document.removeEventListener('visibilitychange', handleVisibility);
		}
	}

	connect();
	return { close };
}
