import { auth } from '$lib/auth.svelte';
import type { Capsule } from '$lib/api/capsules';

export type SSEEvent = {
	event: string;
	timestamp: string;
	team_id: string;
	actor: { type: string; id?: string; name?: string };
	resource: { id: string; type: string };
	sandbox?: Capsule;
};

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

	async function connect() {
		if (closed) return;

		const isAdmin = opts?.admin ?? false;
		const ticket = await fetchTicket(isAdmin);
		if (!ticket || closed) return;

		const basePath = isAdmin ? '/api/v1/admin/events/stream' : '/api/v1/events/stream';
		const url = `${basePath}?ticket=${encodeURIComponent(ticket)}`;

		eventSource = new EventSource(url);

		eventSource.onopen = () => {
			backoff = 1000;
		};

		eventSource.onerror = () => {
			eventSource?.close();
			eventSource = null;
			if (!closed) {
				reconnectTimeout = setTimeout(connect, backoff);
				backoff = Math.min(backoff * 2, 30000);
			}
		};

		eventSource.addEventListener('capsule.created', handleEvent);
		eventSource.addEventListener('capsule.running', handleEvent);
		eventSource.addEventListener('capsule.paused', handleEvent);
		eventSource.addEventListener('capsule.destroyed', handleEvent);
		eventSource.addEventListener('template.snapshot.created', handleEvent);
		eventSource.addEventListener('template.snapshot.deleted', handleEvent);
		eventSource.addEventListener('host.up', handleEvent);
		eventSource.addEventListener('host.down', handleEvent);
	}

	function handleEvent(e: MessageEvent) {
		try {
			const data: SSEEvent = JSON.parse(e.data);
			onEvent(data);
		} catch {
			// Ignore malformed messages.
		}
	}

	function close() {
		closed = true;
		eventSource?.close();
		eventSource = null;
		if (reconnectTimeout) {
			clearTimeout(reconnectTimeout);
			reconnectTimeout = null;
		}
	}

	connect();
	return { close };
}
