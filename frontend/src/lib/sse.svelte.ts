import { connectEventStream, type SSEEvent, type EventStreamConnection } from '$lib/api/events';
import { auth } from '$lib/auth.svelte';

type SSEListener = (event: SSEEvent) => void;

let connection: EventStreamConnection | null = null;
let adminConnection: EventStreamConnection | null = null;
let listeners = new Set<SSEListener>();
let adminListeners = new Set<SSEListener>();
let started = false;
let adminStarted = false;

function dispatch(event: SSEEvent) {
	for (const fn of listeners) {
		fn(event);
	}
}

function adminDispatch(event: SSEEvent) {
	for (const fn of adminListeners) {
		fn(event);
	}
}

function ensureConnected() {
	if (connection || !auth.isAuthenticated) return;
	connection = connectEventStream(dispatch);
}

function ensureAdminConnected() {
	if (adminConnection || !auth.isAuthenticated) return;
	adminConnection = connectEventStream(adminDispatch, { admin: true });
}

export function startSSE() {
	if (started) return;
	started = true;
	ensureConnected();
}

export function stopSSE() {
	started = false;
	connection?.close();
	connection = null;
	listeners.clear();
}

export function startAdminSSE() {
	if (adminStarted) return;
	adminStarted = true;
	ensureAdminConnected();
}

export function stopAdminSSE() {
	adminStarted = false;
	adminConnection?.close();
	adminConnection = null;
	adminListeners.clear();
}

export function subscribeSSE(fn: SSEListener): () => void {
	listeners.add(fn);
	ensureConnected();
	return () => {
		listeners.delete(fn);
	};
}

export function subscribeAdminSSE(fn: SSEListener): () => void {
	adminListeners.add(fn);
	ensureAdminConnected();
	return () => {
		adminListeners.delete(fn);
	};
}
