// build-console-ws.svelte.ts — WebSocket client for the live admin build
// console. Connects to GET /v1/admin/builds/{id}/stream, which replays the
// completed-step history then live-tails events. The client maps events to a
// reactive step list and forwards raw PTY output to a terminal writer.

import { buildStreamUrl, type BuildStreamEvent } from '$lib/api/builds';

export type StepStatus = 'running' | 'success' | 'failed';

export type BuildStep = {
	step: number;
	phase: string;
	cmd: string;
	status: StepStatus;
	exit: number | null;
	elapsedMs: number | null;
};

export type ConsoleConnState = 'connecting' | 'connected' | 'closed' | 'error';

const RECONNECT_DELAY = 1500;

// ANSI truecolor escapes matching the Wrenn palette.
const dim = (s: string) => `\x1b[38;2;107;104;98m${s}\x1b[0m`; // text-tertiary
const sage = (s: string) => `\x1b[38;2;137;167;133m${s}\x1b[0m`; // accent-mid
const red = (s: string) => `\x1b[38;2;207;129;114m${s}\x1b[0m`; // red

// Binary-safe base64 decode for raw PTY bytes.
function decodeBase64(b64: string): string {
	const bytes = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
	return new TextDecoder().decode(bytes);
}

function isTerminal(status: string): boolean {
	return status === 'success' || status === 'failed' || status === 'cancelled';
}

/**
 * createBuildConsole wires a build's event WebSocket to reactive state.
 * Call connect() with a terminal write function once the terminal exists,
 * and disconnect() on teardown.
 */
export function createBuildConsole(buildId: string) {
	let connState = $state<ConsoleConnState>('connecting');
	let steps = $state<BuildStep[]>([]);
	let buildStatus = $state('');
	let currentStep = $state(0);
	let totalSteps = $state(0);
	let errorMessage = $state<string | null>(null);

	let ws: WebSocket | null = null;
	let writeTerm: ((text: string) => void) | null = null;
	let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
	let disposed = false;

	function upsertStep(step: number, patch: Partial<BuildStep>) {
		const idx = steps.findIndex((s) => s.step === step);
		if (idx === -1) {
			steps = [
				...steps,
				{
					step,
					phase: patch.phase ?? '',
					cmd: patch.cmd ?? '',
					status: patch.status ?? 'running',
					exit: patch.exit ?? null,
					elapsedMs: patch.elapsedMs ?? null
				}
			].sort((a, b) => a.step - b.step);
		} else {
			// Immutable replace so the reactive array re-renders the step list.
			steps = steps.map((s, i) => (i === idx ? { ...s, ...patch } : s));
		}
	}

	function summaryLine(status: string): string {
		if (status === 'success') return `\r\n${sage('● build succeeded')}\r\n`;
		if (status === 'failed') return `\r\n${red('● build failed')}\r\n`;
		return `\r\n${dim('● build ' + status)}\r\n`;
	}

	function handle(ev: BuildStreamEvent) {
		switch (ev.type) {
			case 'step-start':
				upsertStep(ev.step ?? 0, {
					phase: ev.phase,
					cmd: ev.cmd,
					status: 'running',
					exit: null,
					elapsedMs: null
				});
				writeTerm?.(`\r\n${sage('▸')} ${dim('step ' + ev.step)}  ${ev.cmd ?? ''}\r\n`);
				break;
			case 'output':
				if (ev.data) writeTerm?.(decodeBase64(ev.data));
				break;
			case 'step-end': {
				const ok = ev.ok ?? false;
				upsertStep(ev.step ?? 0, {
					phase: ev.phase,
					cmd: ev.cmd,
					status: ok ? 'success' : 'failed',
					exit: ev.exit ?? 0,
					elapsedMs: ev.elapsed_ms ?? 0
				});
				// The healthcheck is shown as a trailing pseudo-step but is not
				// counted in total_steps, so it must not advance the counter.
				if (ev.phase !== 'healthcheck' && typeof ev.step === 'number' && ev.step > currentStep) {
					currentStep = ev.step;
				}
				break;
			}
			case 'build-status':
				if (ev.status) buildStatus = ev.status;
				if (typeof ev.total_steps === 'number' && ev.total_steps > 0) totalSteps = ev.total_steps;
				if (typeof ev.current_step === 'number' && ev.current_step > currentStep) {
					currentStep = ev.current_step;
				}
				if (ev.error) errorMessage = ev.error;
				if (ev.status && isTerminal(ev.status)) writeTerm?.(summaryLine(ev.status));
				break;
			case 'ping':
				break;
		}
	}

	function open() {
		connState = 'connecting';
		ws = new WebSocket(buildStreamUrl(buildId));

		ws.onopen = () => {
			connState = 'connected';
		};

		ws.onmessage = (e) => {
			try {
				handle(JSON.parse(e.data) as BuildStreamEvent);
			} catch {
				// ignore malformed frames
			}
		};

		ws.onclose = () => {
			if (disposed) return;
			// A finished build closes cleanly; nothing more to stream.
			if (isTerminal(buildStatus)) {
				connState = 'closed';
				return;
			}
			// Unexpected drop mid-build: reconnect and resume from history.
			connState = 'connecting';
			writeTerm?.(`\r\n${dim('[reconnecting...]')}\r\n`);
			reconnectTimer = setTimeout(open, RECONNECT_DELAY);
		};

		ws.onerror = () => {
			if (!disposed) connState = 'error';
		};
	}

	return {
		get connState() {
			return connState;
		},
		get steps() {
			return steps;
		},
		get buildStatus() {
			return buildStatus;
		},
		get currentStep() {
			return currentStep;
		},
		get totalSteps() {
			return totalSteps;
		},
		get errorMessage() {
			return errorMessage;
		},

		/** connect opens the WebSocket; write receives terminal output. */
		connect(write: (text: string) => void) {
			if (disposed) return;
			writeTerm = write;
			open();
		},

		/** disconnect tears down the WebSocket and cancels any reconnect. */
		disconnect() {
			disposed = true;
			if (reconnectTimer) clearTimeout(reconnectTimer);
			ws?.close();
			ws = null;
		}
	};
}
