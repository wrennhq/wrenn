import type { Capsule } from '$lib/api/capsules';

// Mirror the SSE event names emitted by pkg/events. Keep in sync with the
// `SSEEvent.event` enum in internal/api/openapi.yaml.
export type SSEEventKind =
	| 'connected'
	| 'capsule.create'
	| 'capsule.pause'
	| 'capsule.resume'
	| 'capsule.destroy'
	| 'capsule.state.changed'
	| 'template.snapshot.create'
	| 'template.snapshot.delete'
	| 'host.up'
	| 'host.down';

export type SSEEventOutcome = 'success' | 'error';

export type SSEEvent = {
	event: SSEEventKind;
	outcome?: SSEEventOutcome;
	timestamp: string;
	team_id: string;
	actor: { type: string; id?: string; name?: string };
	resource: { id: string; type: string };
	metadata?: Record<string, string>;
	error?: string;
	sandbox?: Capsule | null;
};

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

/**
 * Connects to the SSE event stream. Returns a handle to close the connection.
 * Automatically reconnects on disconnect with exponential backoff. The
 * browser sends the wrenn_sid cookie automatically on EventSource so no
 * ticket exchange is required.
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

	function connect() {
		if (closed) return;

		const isAdmin = opts?.admin ?? false;
		const url = isAdmin ? '/api/v1/admin/events/stream' : '/api/v1/events/stream';

		eventSource = new EventSource(url, { withCredentials: true });

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

		eventSource.addEventListener('capsule.create', handleEvent);
		eventSource.addEventListener('capsule.pause', handleEvent);
		eventSource.addEventListener('capsule.resume', handleEvent);
		eventSource.addEventListener('capsule.destroy', handleEvent);
		eventSource.addEventListener('capsule.state.changed', handleEvent);
		eventSource.addEventListener('template.snapshot.create', handleEvent);
		eventSource.addEventListener('template.snapshot.delete', handleEvent);
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
