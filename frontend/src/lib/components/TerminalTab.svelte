<script lang="ts">
	import { onDestroy, tick } from 'svelte';
	import { auth } from '$lib/auth.svelte';

	type Props = {
		capsuleId: string;
		isRunning: boolean;
		visible?: boolean;
		apiBasePath?: string;
	};

	let { capsuleId, isRunning, visible = true, apiBasePath = '/api/v1/capsules' }: Props = $props();

	type ConnectionState = 'idle' | 'connecting' | 'connected' | 'disconnected' | 'error';

	type SessionDisplay = {
		id: number;
		state: ConnectionState;
		errorMessage: string | null;
		ptyTag: string | null;
		ptyPid: number | null;
	};

	type SessionInternal = {
		// eslint-disable-next-line @typescript-eslint/no-explicit-any
		term: any;
		// eslint-disable-next-line @typescript-eslint/no-explicit-any
		fitAddon: any;
		ws: WebSocket | null;
		resizeObserver: ResizeObserver | null;
		fitDebounce: ReturnType<typeof setTimeout> | null;
		inputFlushTimer: ReturnType<typeof setTimeout> | null;
		inputBuffer: string;
	};

	const MAX_SESSIONS = 8;

	let sessions = $state<SessionDisplay[]>([]);
	const internals = new Map<number, SessionInternal>();
	let activeSessionId = $state<number | null>(null);
	let nextId = 0;
	let cssLoaded = false;
	let containerRef = $state<HTMLDivElement | undefined>(undefined);
	let hasAutoCreated = false;

	// eslint-disable-next-line @typescript-eslint/no-explicit-any
	let TerminalClass: any = null;
	// eslint-disable-next-line @typescript-eslint/no-explicit-any
	let FitAddonClass: any = null;
	// eslint-disable-next-line @typescript-eslint/no-explicit-any
	let WebLinksAddonClass: any = null;

	const activeSession = $derived(sessions.find(s => s.id === activeSessionId) ?? null);

	const TERM_THEME = {
		background: '#0a0c0b',
		foreground: '#d0cdc6',
		cursor: '#5e8c58',
		cursorAccent: '#0a0c0b',
		selectionBackground: 'rgba(94, 140, 88, 0.25)',
		selectionForeground: '#eae7e2',
		selectionInactiveBackground: 'rgba(94, 140, 88, 0.12)',
		black: '#1a1e1c',
		red: '#cf8172',
		green: '#5e8c58',
		yellow: '#d4a73c',
		blue: '#5a9fd4',
		magenta: '#b07ab8',
		cyan: '#5aafb0',
		white: '#d0cdc6',
		brightBlack: '#454340',
		brightRed: '#e09585',
		brightGreen: '#89a785',
		brightYellow: '#e0c070',
		brightBlue: '#7ab8e0',
		brightMagenta: '#c898cf',
		brightCyan: '#7ac5c6',
		brightWhite: '#eae7e2',
	};

	// Binary-safe base64 encode (handles multi-byte UTF-8 from xterm onData)
	function toBase64(str: string): string {
		return btoa(
			Array.from(new TextEncoder().encode(str), (b) => String.fromCharCode(b)).join('')
		);
	}

	// Binary-safe base64 decode (handles raw PTY bytes)
	function fromBase64(b64: string): string {
		const bytes = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
		return new TextDecoder().decode(bytes);
	}

	function getWsUrl(): string {
		const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
		return `${proto}//${window.location.host}${apiBasePath}/${capsuleId}/pty`;
	}

	function wsSend(ws: WebSocket | null, data: string) {
		try {
			if (ws?.readyState === WebSocket.OPEN) ws.send(data);
		} catch {
			// Connection closing — ignore
		}
	}

	function updateSession(id: number, updates: Partial<SessionDisplay>) {
		const idx = sessions.findIndex(s => s.id === id);
		if (idx === -1) return;
		Object.assign(sessions[idx], updates);
	}

	async function loadModules() {
		if (TerminalClass) return;
		const [{ Terminal }, { FitAddon }, { WebLinksAddon }] = await Promise.all([
			import('@xterm/xterm'),
			import('@xterm/addon-fit'),
			import('@xterm/addon-web-links')
		]);
		TerminalClass = Terminal;
		FitAddonClass = FitAddon;
		WebLinksAddonClass = WebLinksAddon;
		if (!cssLoaded) {
			await import('@xterm/xterm/css/xterm.css');
			cssLoaded = true;
		}
	}

	// Create first session when the tab becomes visible for the first time
	$effect(() => {
		if (visible && isRunning && !hasAutoCreated && containerRef) {
			hasAutoCreated = true;
			createSession();
		}
	});

	// Re-fit active terminal when tab becomes visible (after being hidden)
	$effect(() => {
		if (visible && activeSessionId !== null) {
			const int = internals.get(activeSessionId);
			if (int?.fitAddon && int.term) {
				requestAnimationFrame(() => {
					int.fitAddon.fit();
					int.term.focus();
				});
			}
		}
	});

	// Close all sessions when capsule stops running
	$effect(() => {
		if (!isRunning && sessions.length > 0) {
			// Copy IDs to avoid mutating during iteration
			const ids = sessions.map(s => s.id);
			for (const id of ids) closeSession(id);
		}
	});

	async function createSession() {
		if (!isRunning || !containerRef) return;
		if (sessions.length >= MAX_SESSIONS) return;
		await loadModules();

		const id = nextId++;

		sessions = [...sessions, {
			id,
			state: 'connecting',
			errorMessage: null,
			ptyTag: null,
			ptyPid: null,
		}];
		activeSessionId = id;

		await tick();

		const el = containerRef?.querySelector(`[data-session-id="${id}"]`) as HTMLDivElement | null;
		if (!el) {
			// DOM didn't render — clean up the orphaned display entry
			sessions = sessions.filter(s => s.id !== id);
			if (activeSessionId === id) activeSessionId = null;
			return;
		}

		const fitAddon = new FitAddonClass();
		const term = new TerminalClass({
			cursorBlink: true,
			cursorStyle: 'bar',
			cursorInactiveStyle: 'outline',
			fontFamily: "'JetBrains Mono Variable', 'JetBrains Mono', monospace",
			fontSize: 14,
			lineHeight: 1.35,
			letterSpacing: 0,
			theme: TERM_THEME,
			allowProposedApi: true,
			scrollback: 5000,
			convertEol: true,
		});

		term.loadAddon(fitAddon);
		term.loadAddon(new WebLinksAddonClass());
		term.open(el);

		const internal: SessionInternal = {
			term,
			fitAddon,
			ws: null,
			resizeObserver: null,
			fitDebounce: null,
			inputFlushTimer: null,
			inputBuffer: '',
		};
		internals.set(id, internal);

		requestAnimationFrame(() => fitAddon.fit());

		internal.resizeObserver = new ResizeObserver(() => {
			if (internal.fitDebounce) clearTimeout(internal.fitDebounce);
			internal.fitDebounce = setTimeout(() => {
				if (internal.fitAddon && internal.term && activeSessionId === id) {
					internal.fitAddon.fit();
				}
			}, 50);
		});
		internal.resizeObserver.observe(el);

		// Register input/resize handlers ONCE per terminal (not per connection).
		function flushInput() {
			const int = internals.get(id);
			if (!int) return;
			int.inputFlushTimer = null;
			if (!int.inputBuffer) return;
			wsSend(int.ws, JSON.stringify({ type: 'input', data: toBase64(int.inputBuffer) }));
			int.inputBuffer = '';
		}

		term.onData((data: string) => {
			const int = internals.get(id);
			if (!int) return;
			int.inputBuffer += data;
			if (!int.inputFlushTimer) {
				int.inputFlushTimer = setTimeout(flushInput, 50);
			}
		});

		term.onResize(({ cols, rows }: { cols: number; rows: number }) => {
			const i = internals.get(id);
			wsSend(i?.ws ?? null, JSON.stringify({ type: 'resize', cols, rows }));
		});

		connectSession(id);
	}

	function connectSession(id: number, reconnectTag?: string) {
		const int = internals.get(id);
		if (!int) return;

		if (!auth.token) {
			updateSession(id, { state: 'error', errorMessage: 'Not authenticated' });
			return;
		}

		const display = sessions.find(s => s.id === id);
		const tag = reconnectTag ?? display?.ptyTag;

		const ws = new WebSocket(getWsUrl());
		int.ws = ws;
		updateSession(id, { state: 'connecting', errorMessage: null });

		ws.onopen = () => {
			// Send auth as the first message (JWT no longer in URL).
			wsSend(ws, JSON.stringify({ type: 'auth', token: auth.token }));
			const { cols, rows } = int.term;
			const msg: Record<string, unknown> = {
				type: tag ? 'connect' : 'start',
				cols,
				rows,
			};
			if (tag) {
				msg.tag = tag;
			} else {
				msg.cmd = '/bin/bash';
				msg.envs = { TERM: 'xterm-256color' };
			}
			wsSend(ws, JSON.stringify(msg));
		};

		ws.onmessage = (event) => {
			try {
				const msg = JSON.parse(event.data);
				switch (msg.type) {
					case 'started':
						updateSession(id, {
							state: 'connected',
							ptyTag: msg.tag,
							ptyPid: msg.pid ?? null,
						});
						if (activeSessionId === id) int.term.focus();
						break;
					case 'output':
						if (msg.data) int.term.write(fromBase64(msg.data));
						break;
					case 'exit':
						closeSession(id);
						break;
					case 'error':
						if (msg.fatal) {
							updateSession(id, { state: 'error', errorMessage: msg.data || 'Connection error' });
							int.term.write(`\r\n\x1b[38;2;207;129;114m${msg.data}\x1b[0m\r\n`);
						}
						break;
					case 'ping':
						wsSend(ws, JSON.stringify({ type: 'pong' }));
						break;
				}
			} catch {
				// Ignore malformed messages
			}
		};

		ws.onclose = (event) => {
			const s = sessions.find(s => s.id === id);
			if (!s) return;

			// Abnormal close with a live session — auto-reconnect
			if (!event.wasClean && s.state === 'connected' && s.ptyTag) {
				updateSession(id, { state: 'connecting', errorMessage: null });
				int.term.write('\r\n\x1b[38;2;107;104;98m[reconnecting...]\x1b[0m\r\n');
				setTimeout(() => connectSession(id, s.ptyTag ?? undefined), 1000);
				return;
			}

			if (s.state === 'connected') {
				updateSession(id, { state: 'disconnected' });
			}
		};

		ws.onerror = () => {
			updateSession(id, { state: 'error', errorMessage: 'Connection lost — check that the capsule is running' });
		};
	}

	function switchTo(id: number) {
		activeSessionId = id;
		requestAnimationFrame(() => {
			const int = internals.get(id);
			if (int?.fitAddon && int.term) {
				int.fitAddon.fit();
				int.term.focus();
			}
		});
	}

	function closeSession(id: number) {
		const idx = sessions.findIndex(s => s.id === id);
		if (idx === -1) return;

		const int = internals.get(id);
		if (int) {
			if (int.fitDebounce) clearTimeout(int.fitDebounce);
			if (int.inputFlushTimer) clearTimeout(int.inputFlushTimer);
			int.resizeObserver?.disconnect();
			wsSend(int.ws, JSON.stringify({ type: 'kill' }));
			int.ws?.close();
			int.term?.dispose();
			internals.delete(id);
		}

		sessions = sessions.filter(s => s.id !== id);

		if (activeSessionId === id) {
			if (sessions.length === 0) {
				activeSessionId = null;
			} else {
				const newIdx = Math.min(idx, sessions.length - 1);
				switchTo(sessions[newIdx].id);
			}
		}
	}

	function reconnectSession(id: number) {
		const int = internals.get(id);
		const display = sessions.find(s => s.id === id);
		if (!int || !display) return;
		int.ws?.close();
		connectSession(id, display.ptyTag ?? undefined);
	}

	function statusDot(state: ConnectionState): string {
		switch (state) {
			case 'connected': return 'bg-[var(--color-accent)]';
			case 'connecting': return 'bg-[var(--color-text-tertiary)] animate-pulse';
			case 'error': return 'bg-[var(--color-red)]';
			default: return 'bg-[var(--color-text-muted)]';
		}
	}

	onDestroy(() => {
		for (const [, int] of internals) {
			if (int.fitDebounce) clearTimeout(int.fitDebounce);
			if (int.inputFlushTimer) clearTimeout(int.inputFlushTimer);
			int.resizeObserver?.disconnect();
			int.ws?.close();
			int.term?.dispose();
		}
		internals.clear();
	});
</script>

<style>
	.terminal-container :global(.xterm) {
		padding: 12px 4px 12px 16px;
		height: 100%;
	}
	.terminal-container :global(.xterm-viewport),
	.terminal-container :global(.xterm-screen) {
		background-color: #0a0c0b !important;
	}
	.terminal-container :global(.xterm-viewport) {
		scrollbar-width: thin;
		scrollbar-color: rgba(94, 140, 88, 0.18) transparent;
	}
	.terminal-container :global(.xterm-viewport::-webkit-scrollbar) {
		width: 6px;
	}
	.terminal-container :global(.xterm-viewport::-webkit-scrollbar-track) {
		background: transparent;
	}
	.terminal-container :global(.xterm-viewport::-webkit-scrollbar-thumb) {
		background: rgba(94, 140, 88, 0.18);
		border-radius: 3px;
	}
	.terminal-container :global(.xterm-viewport::-webkit-scrollbar-thumb:hover) {
		background: rgba(94, 140, 88, 0.32);
	}
	.tab-scroll {
		scrollbar-width: none;
	}
	.tab-scroll::-webkit-scrollbar {
		display: none;
	}
	.term-tab {
		position: relative;
	}
	.term-tab::after {
		content: '';
		position: absolute;
		right: 0;
		top: 25%;
		bottom: 25%;
		width: 1px;
		background: var(--color-border);
	}
	.term-tab:last-child::after {
		display: none;
	}
	.term-tab-active::after {
		display: none;
	}
	.term-tab:has(+ .term-tab-active)::after {
		display: none;
	}
</style>

<div class="flex flex-1 flex-col min-h-0">
	{#if !isRunning}
		<div class="flex flex-1 items-center justify-center">
			<div class="flex flex-col items-center gap-5 text-center">
				<div class="flex h-16 w-16 items-center justify-center rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)]" style="animation: iconFloat 3s ease-in-out infinite">
					<svg class="text-[var(--color-text-muted)]" width="28" height="28" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
						<polyline points="4 17 10 11 4 5" /><line x1="12" y1="19" x2="20" y2="19" />
					</svg>
				</div>
				<div class="flex flex-col gap-1.5">
					<span class="text-body font-medium text-[var(--color-text-secondary)]">Terminal unavailable</span>
					<span class="text-ui text-[var(--color-text-muted)]">Start the capsule to connect</span>
				</div>
			</div>
		</div>
	{:else}
		<!-- Unified session bar (hidden when no sessions) -->
		<div class="flex items-stretch bg-[var(--color-bg-1)]" style:display={sessions.length === 0 ? 'none' : 'flex'}>
			<div class="tab-scroll flex items-stretch overflow-x-auto">
				{#each sessions as session (session.id)}
					<!-- svelte-ignore a11y_no_static_element_interactions -->
					<div
						onclick={() => switchTo(session.id)}
						onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') switchTo(session.id); }}
						role="tab"
						tabindex="0"
						aria-selected={session.id === activeSessionId}
						class="term-tab group flex shrink-0 cursor-pointer items-center gap-2.5 px-5 py-2.5 text-meta transition-colors
							{session.id === activeSessionId
								? 'term-tab-active bg-[var(--color-bg-0)] text-[var(--color-text-primary)]'
								: 'bg-[var(--color-bg-1)] text-[var(--color-text-tertiary)] hover:bg-[var(--color-bg-2)] hover:text-[var(--color-text-secondary)] border-b border-b-[var(--color-border)]'}"
					>
						{#if session.state === 'connected'}
							<span class="relative flex h-[7px] w-[7px] shrink-0">
								<span class="animate-status-ping absolute inline-flex h-full w-full rounded-full bg-[var(--color-accent)]"></span>
								<span class="relative inline-flex h-[7px] w-[7px] rounded-full bg-[var(--color-accent)]"></span>
							</span>
						{:else if session.state === 'connecting'}
							<svg class="animate-spin shrink-0 text-[var(--color-text-tertiary)]" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><path d="M21 12a9 9 0 1 1-6.219-8.56" /></svg>
						{:else if session.state === 'error'}
							<span class="h-[7px] w-[7px] shrink-0 rounded-full bg-[var(--color-red)]"></span>
						{:else}
							<span class="h-[7px] w-[7px] shrink-0 rounded-full bg-[var(--color-text-muted)]"></span>
						{/if}

						<span class="font-mono">
							bash{#if session.ptyPid}<span class="text-[var(--color-text-muted)]">:{session.ptyPid}</span>{/if}
						</span>

						<button
							onclick={(e) => { e.stopPropagation(); closeSession(session.id); }}
							class="ml-0.5 flex h-5 w-5 items-center justify-center rounded-[3px] text-[var(--color-text-muted)] opacity-0 transition-all group-hover:opacity-100 hover:bg-[var(--color-bg-4)] hover:text-[var(--color-text-secondary)]"
							title="Close session"
						>
							<svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
								<line x1="18" y1="6" x2="6" y2="18" /><line x1="6" y1="6" x2="18" y2="18" />
							</svg>
						</button>
					</div>
				{/each}
			</div>

			<button
				onclick={createSession}
				disabled={sessions.length >= MAX_SESSIONS}
				class="flex shrink-0 items-center justify-center aspect-square self-stretch border-b border-[var(--color-border)] text-[var(--color-text-tertiary)] transition-colors hover:bg-[var(--color-bg-2)] hover:text-[var(--color-text-primary)] disabled:opacity-30 disabled:cursor-not-allowed disabled:hover:bg-transparent disabled:hover:text-[var(--color-text-tertiary)]"
				title={sessions.length >= MAX_SESSIONS ? `Maximum ${MAX_SESSIONS} sessions` : 'New terminal session'}
			>
				<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
					<line x1="12" y1="5" x2="12" y2="19" /><line x1="5" y1="12" x2="19" y2="12" />
				</svg>
			</button>

			<div class="flex-1 border-b border-[var(--color-border)] bg-[var(--color-bg-1)]"></div>

			{#if activeSession}
				<div class="flex items-center gap-3 border-b border-[var(--color-border)] bg-[var(--color-bg-1)] pr-4">
					{#if activeSession.state === 'error' && activeSession.errorMessage}
						<span class="text-meta text-[var(--color-red)]/70">{activeSession.errorMessage}</span>
					{/if}

					{#if (activeSession.state === 'disconnected' || activeSession.state === 'error') && activeSession.ptyTag}
						<button
							onclick={() => activeSession && reconnectSession(activeSession.id)}
							class="flex items-center gap-1.5 rounded-[var(--radius-button)] border border-[var(--color-border)] bg-[var(--color-bg-3)] px-3 py-1 text-meta font-medium text-[var(--color-text-secondary)] transition-colors hover:bg-[var(--color-bg-4)] hover:text-[var(--color-text-primary)]"
						>
							<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
								<polyline points="1 4 1 10 7 10" /><polyline points="23 20 23 14 17 14" />
								<path d="M20.49 9A9 9 0 0 0 5.64 5.64L1 10m22 4l-4.64 4.36A9 9 0 0 1 3.51 15" />
							</svg>
							Reconnect
						</button>
					{/if}

					{#if activeSession.ptyTag}
						<span class="font-mono text-label text-[var(--color-text-muted)]">{activeSession.ptyTag}</span>
					{/if}
				</div>
			{/if}
		</div>

		<!-- Terminal surfaces -->
		<div class="relative flex-1 min-h-0 bg-[var(--color-bg-0)]" bind:this={containerRef}>
			{#each sessions as session (session.id)}
				<div
					data-session-id={session.id}
					class="terminal-container absolute inset-0 bg-[var(--color-bg-0)]"
					style:display={session.id === activeSessionId ? 'block' : 'none'}
				></div>
			{/each}

			{#if sessions.length === 0}
				<div class="flex h-full items-center justify-center">
					<div class="flex flex-col items-center gap-5 text-center">
						<div class="flex h-16 w-16 items-center justify-center rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)]" style="animation: iconFloat 3s ease-in-out infinite">
							<svg class="text-[var(--color-text-muted)]" width="28" height="28" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
								<polyline points="4 17 10 11 4 5" /><line x1="12" y1="19" x2="20" y2="19" />
							</svg>
						</div>
						<div class="flex flex-col gap-1.5">
							<span class="text-body font-medium text-[var(--color-text-secondary)]">No active sessions</span>
							<span class="text-ui text-[var(--color-text-muted)]">All terminal sessions have been closed</span>
						</div>
						<button
							onclick={createSession}
							class="mt-1 flex items-center gap-2 rounded-[var(--radius-button)] border border-[var(--color-accent)]/30 bg-[var(--color-accent-glow-mid)] px-5 py-2.5 text-ui font-semibold text-[var(--color-accent-bright)] transition-all duration-150 hover:border-[var(--color-accent)]/50 hover:bg-[var(--color-accent)]/15 hover:-translate-y-px active:translate-y-0"
						>
							<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
								<line x1="12" y1="5" x2="12" y2="19" /><line x1="5" y1="12" x2="19" y2="12" />
							</svg>
							New session
						</button>
					</div>
				</div>
			{/if}
		</div>
	{/if}
</div>
