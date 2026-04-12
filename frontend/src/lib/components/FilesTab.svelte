<script lang="ts">
	import { onDestroy } from 'svelte';
	import {
		listDir,
		readFile,
		downloadFile,
		isBinaryFile,
		isFileTooLarge,
		formatFileSize,
		type FileEntry,
	} from '$lib/api/files';
	import { tokenize, type ThemedToken } from '$lib/highlight';

	type Props = {
		capsuleId: string;
		isRunning: boolean;
		apiBasePath?: string;
		/** Hide the file preview pane when no file is selected */
		compact?: boolean;
		/** Show only the file tree, completely removing the preview panel */
		treeOnly?: boolean;
	};

	let { capsuleId, isRunning, apiBasePath = '/api/v1/capsules', compact = false, treeOnly = false }: Props = $props();

	// Directory navigation state
	let currentPath = $state('~');
	let entries = $state<FileEntry[]>([]);
	let dirLoading = $state(false);
	let dirError = $state<string | null>(null);

	// File preview state
	let selectedFile = $state<FileEntry | null>(null);
	let fileContent = $state<string | null>(null);
	let fileLoading = $state(false);
	let fileError = $state<string | null>(null);
	let downloading = $state(false);

	// Syntax highlighting (lazy — loaded on first use)
	let highlightedTokens = $state<ThemedToken[][] | null>(null);

	// Request generation counters — discard stale responses from rapid clicks
	let dirGeneration = 0;
	let fileGeneration = 0;

	// AbortController for in-flight file reads — aborted when the user
	// selects a different file or the component is torn down.
	let fileAbort: AbortController | null = null;

	onDestroy(() => {
		fileAbort?.abort();
	});

	const MAX_PREVIEW_LINES = 5000;
	const MAX_HIGHLIGHT_LINES = 2000; // Don't tokenize huge files — diminishing returns

	// Path input
	let pathInput = $state('~');
	let pathInputFocused = $state(false);
	let pathInputEl = $state<HTMLInputElement | undefined>(undefined);

	// Pre-computed preview lines — avoids re-splitting on every render
	const previewLines = $derived.by(() => {
		if (!fileContent) return { lines: [] as string[], truncated: false, totalLines: 0 };
		const allLines = fileContent.split('\n');
		const truncated = allLines.length > MAX_PREVIEW_LINES;
		return {
			lines: truncated ? allLines.slice(0, MAX_PREVIEW_LINES) : allLines,
			truncated,
			totalLines: allLines.length,
		};
	});

	// Sorted entries: directories first, then files, alphabetical within each group
	const sortedEntries = $derived(
		[...entries].sort((a, b) => {
			if (a.type === 'directory' && b.type !== 'directory') return -1;
			if (a.type !== 'directory' && b.type === 'directory') return 1;
			return a.name.localeCompare(b.name);
		})
	);

	// Breadcrumb segments from currentPath
	const breadcrumbs = $derived(() => {
		const parts = currentPath.split('/').filter(Boolean);
		const crumbs: { name: string; path: string }[] = [{ name: '/', path: '/' }];
		for (let i = 0; i < parts.length; i++) {
			crumbs.push({ name: parts[i], path: '/' + parts.slice(0, i + 1).join('/') });
		}
		return crumbs;
	});

	// Count of dirs vs files for the footer
	const dirCount = $derived(entries.filter((e) => e.type === 'directory').length);
	const fileCount = $derived(entries.filter((e) => e.type !== 'directory').length);

	const canGoUp = $derived(currentPath !== '/' && currentPath.startsWith('/'));

	// Only regular files can be downloaded — symlinks and other non-regular types
	// may point to devices, sockets, or directories that can't be read as a file.
	const isDownloadable = $derived(selectedFile?.type === 'file');

	// Device files, pipes, sockets, etc. — can't be read or downloaded.
	const isSpecialFile = $derived(selectedFile?.type === 'unknown');

	async function navigateTo(path: string) {
		// Abort any in-flight file read and invalidate stale generation so the
		// abort error isn't surfaced in the UI.
		fileAbort?.abort();
		++fileGeneration;
		currentPath = normalizePath(path);
		pathInput = currentPath;
		selectedFile = null;
		fileContent = null;
		fileError = null;
		highlightedTokens = null;
		await loadDir();
	}

	function normalizePath(p: string): string {
		// Let envd handle ~ expansion — pass through as-is
		if (p === '~' || p.startsWith('~/')) {
			return p;
		}

		if (!p.startsWith('/')) {
			// Relative path — resolve against current directory
			p = currentPath.replace(/\/$/, '') + '/' + p;
		}
		// Collapse .. and .
		const parts = p.split('/').filter(Boolean);
		const resolved: string[] = [];
		for (const part of parts) {
			if (part === '..') resolved.pop();
			else if (part !== '.') resolved.push(part);
		}
		return '/' + resolved.join('/');
	}

	/** Derive the parent directory from an entry's absolute path. */
	function parentFromEntry(entryPath: string): string {
		const lastSlash = entryPath.lastIndexOf('/');
		if (lastSlash <= 0) return '/';
		return entryPath.slice(0, lastSlash);
	}

	async function loadDir() {
		if (!isRunning) return;
		dirLoading = true;
		dirError = null;
		const gen = ++dirGeneration;
		const result = await listDir(capsuleId, currentPath, 1, apiBasePath);
		if (gen !== dirGeneration) return; // stale response
		if (result.ok) {
			entries = result.data.entries ?? [];
			// Resolve actual path when envd expanded ~ or a relative path
			if (!currentPath.startsWith('/') && entries.length > 0) {
				currentPath = parentFromEntry(entries[0].path);
				pathInput = currentPath;
			}
		} else {
			dirError = result.error;
			entries = [];
		}
		dirLoading = false;
	}

	async function selectFile(entry: FileEntry) {
		if (entry.type === 'directory') {
			await navigateTo(entry.path);
			return;
		}

		// Abort any in-flight file read before starting a new one.
		fileAbort?.abort();

		selectedFile = entry;
		fileContent = null;
		fileError = null;
		highlightedTokens = null;

		// Non-regular files (devices, pipes, sockets) — nothing to read
		if (entry.type === 'unknown') {
			return;
		}

		// Check if we should preview or prompt download
		if (isBinaryFile(entry.name) || isFileTooLarge(entry.size)) {
			// Don't load content — the preview pane will show download prompt
			return;
		}

		fileLoading = true;
		const gen = ++fileGeneration;
		const controller = new AbortController();
		fileAbort = controller;
		try {
			const result = await readFile(capsuleId, entry.path, controller.signal, apiBasePath);
			if (gen !== fileGeneration) return; // stale response — user clicked another file
			if (result.ok) {
				if (looksLikeBinary(result.data)) {
					fileContent = null;
				} else {
					fileContent = result.data;
					// Kick off highlighting in the background — preview shows plain text immediately.
					// Only tokenize up to MAX_HIGHLIGHT_LINES to avoid freezing on large files.
					const linesToHighlight = result.data.split('\n').length > MAX_HIGHLIGHT_LINES
						? result.data.split('\n').slice(0, MAX_HIGHLIGHT_LINES).join('\n')
						: result.data;
					tokenize(linesToHighlight, entry.name).then((tokens) => {
						if (gen === fileGeneration) highlightedTokens = tokens;
					});
				}
			} else if (result.error !== 'Request aborted') {
				fileError = result.error;
			}
		} finally {
			if (gen === fileGeneration) fileLoading = false;
		}
	}

	function looksLikeBinary(text: string): boolean {
		// Sample first 8KB for null bytes or high ratio of non-printable chars
		const sample = text.slice(0, 8192);
		let nonPrintable = 0;
		for (let i = 0; i < sample.length; i++) {
			const code = sample.charCodeAt(i);
			if (code === 0) return true;
			if (code < 32 && code !== 9 && code !== 10 && code !== 13) nonPrintable++;
		}
		return sample.length > 0 && nonPrintable / sample.length > 0.1;
	}

	async function handleDownload() {
		if (!selectedFile || downloading || selectedFile.type !== 'file') return;
		downloading = true;
		try {
			await downloadFile(capsuleId, selectedFile.path, selectedFile.name, undefined, apiBasePath);
		} catch {
			fileError = 'Download failed';
		}
		downloading = false;
	}

	function handlePathSubmit(e: SubmitEvent) {
		e.preventDefault();
		const target = pathInput.trim();
		if (!target) return;
		const resolved = normalizePath(target);
		navigateOrOpenFile(resolved);
	}

	async function navigateOrOpenFile(path: string) {
		// First try as directory
		const dirResult = await listDir(capsuleId, path, 1, apiBasePath);
		if (dirResult.ok) {
			// Resolve actual path from entries (handles ~ expansion by envd)
			const resolvedEntries = dirResult.data.entries ?? [];
			let resolvedPath = path;
			if (resolvedEntries.length > 0) {
				// Derive parent dir from first entry's absolute path
				const firstPath = resolvedEntries[0].path;
				const lastSlash = firstPath.lastIndexOf('/');
				if (lastSlash >= 0) {
					resolvedPath = lastSlash === 0 ? '/' : firstPath.slice(0, lastSlash);
				}
			}
			currentPath = resolvedPath;
			pathInput = resolvedPath;
			entries = resolvedEntries;
			selectedFile = null;
			fileContent = null;
			fileError = null;
			return;
		}

		// If directory listing failed, try reading as a file
		// We need the parent dir to get the file entry info
		const lastSlash = path.lastIndexOf('/');
		const parentPath = lastSlash <= 0 ? '/' : path.slice(0, lastSlash);
		const fileName = path.slice(lastSlash + 1);

		// Navigate to parent directory
		currentPath = parentPath;
		pathInput = parentPath;
		const parentResult = await listDir(capsuleId, parentPath, 1, apiBasePath);
		if (parentResult.ok) {
			entries = parentResult.data.entries ?? [];
			// Find the file in parent listing
			const found = entries.find((e) => e.name === fileName);
			if (found && found.type !== 'directory') {
				await selectFile(found);
			} else {
				dirError = `Not found: ${path}`;
			}
		} else {
			dirError = parentResult.error;
			entries = [];
		}
	}

	function handleKeydown(e: KeyboardEvent) {
		if (e.key === 'Escape') {
			(e.target as HTMLInputElement)?.blur();
		}
	}

	function fileIcon(entry: FileEntry): string {
		if (entry.type === 'directory') return 'dir';
		if (entry.type === 'symlink') return 'link';
		if (entry.type === 'unknown') return 'special';
		return 'file';
	}

	// File extension for subtle coloring
	function fileExt(name: string): string {
		const dot = name.lastIndexOf('.');
		return dot > 0 ? name.slice(dot + 1).toLowerCase() : '';
	}

	// Extension → color mapping for file icons and badges
	function extColor(name: string): string {
		const ext = fileExt(name);
		switch (ext) {
			case 'go': case 'mod': case 'sum':
				return '#5a9fd4';           // blue — Go
			case 'py': case 'pyi': case 'pyx':
				return '#d4a73c';           // amber — Python
			case 'js': case 'mjs': case 'cjs':
				return '#d4a73c';           // amber — JavaScript
			case 'ts': case 'mts': case 'cts': case 'tsx': case 'jsx':
				return '#5a9fd4';           // blue — TypeScript/React
			case 'rs':
				return '#cf8172';           // red — Rust
			case 'sh': case 'bash': case 'zsh': case 'fish':
				return '#5e8c58';           // accent — shell
			case 'json': case 'yaml': case 'yml': case 'toml': case 'ini': case 'env':
				return '#8b7ec8';           // purple — config
			case 'md': case 'mdx': case 'txt': case 'rst':
				return 'var(--color-text-secondary)'; // neutral — docs
			case 'sql':
				return '#5a9fd4';           // blue — SQL
			case 'proto':
				return '#5e8c58';           // accent — protobuf
			case 'svelte': case 'vue':
				return '#cf8172';           // red — Svelte/Vue
			case 'css': case 'scss': case 'less':
				return '#5a9fd4';           // blue — styles
			case 'html': case 'htm':
				return '#cf8172';           // red — HTML
			case 'dockerfile': case 'makefile':
				return '#5e8c58';           // accent — build
			default:
				return 'var(--color-text-muted)';
		}
	}

	// Descriptive label for file type badge in preview header
	function extLabel(name: string): string {
		const ext = fileExt(name);
		const lower = name.toLowerCase();
		if (lower === 'makefile') return 'Make';
		if (lower === 'dockerfile') return 'Docker';
		switch (ext) {
			case 'go': return 'Go';
			case 'py': return 'Python';
			case 'js': case 'mjs': case 'cjs': return 'JS';
			case 'ts': case 'mts': case 'cts': return 'TS';
			case 'tsx': return 'TSX';
			case 'jsx': return 'JSX';
			case 'rs': return 'Rust';
			case 'sh': case 'bash': return 'Shell';
			case 'json': return 'JSON';
			case 'yaml': case 'yml': return 'YAML';
			case 'toml': return 'TOML';
			case 'sql': return 'SQL';
			case 'proto': return 'Proto';
			case 'svelte': return 'Svelte';
			case 'css': return 'CSS';
			case 'html': case 'htm': return 'HTML';
			case 'md': case 'mdx': return 'Markdown';
			default: return ext ? ext.toUpperCase() : '';
		}
	}

	// Load initial directory on mount, falling back to / if home can't be resolved
	let hasInitiallyLoaded = false;
	$effect(() => {
		if (isRunning && !hasInitiallyLoaded) {
			hasInitiallyLoaded = true;
			loadDir().then(() => {
				if (!currentPath.startsWith('/')) {
					currentPath = '/';
					pathInput = '/';
					if (dirError) loadDir();
				}
			});
		}
	});
</script>

<style>
	.file-row {
		transition: background-color 0.1s ease;
	}
	.file-row:hover {
		background-color: var(--color-bg-3);
	}
	.file-row.active {
		background-color: var(--color-accent-glow);
		border-left: 3px solid var(--color-accent);
		box-shadow: inset 0 0 20px rgba(94, 140, 88, 0.06);
	}
	.file-row:not(.active) {
		border-left: 3px solid transparent;
	}

	.preview-code {
		tab-size: 4;
		-moz-tab-size: 4;
	}

	/* Let the browser skip rendering off-screen lines in long files */
	.code-line {
		content-visibility: auto;
		contain-intrinsic-size: auto 1.65rem;
	}

	/* Staggered row entrance */
	@keyframes rowSlideIn {
		from { opacity: 0; transform: translateX(-4px); }
		to   { opacity: 1; transform: translateX(0); }
	}
	.row-enter {
		animation: rowSlideIn 0.15s ease both;
	}

	/* Line highlight on hover */
	.code-line:hover .line-content {
		background-color: var(--color-bg-3);
	}
	.code-line:hover .line-num {
		color: var(--color-text-tertiary);
	}
</style>

{#if !isRunning}
	<div class="flex flex-1 items-center justify-center">
		<div class="flex flex-col items-center gap-4 text-center">
			<div class="flex h-14 w-14 items-center justify-center rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)]" style="animation: iconFloat 3s ease-in-out infinite">
				<svg class="text-[var(--color-text-muted)]" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
					<path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z" />
				</svg>
			</div>
			<div class="flex flex-col gap-1">
				<span class="text-ui font-medium text-[var(--color-text-secondary)]">File browser unavailable</span>
				<span class="text-meta text-[var(--color-text-muted)]">Start the capsule to browse its filesystem</span>
			</div>
		</div>
	</div>
{:else}
	<div class="flex flex-1 min-h-0">

		<!-- Left panel: File tree -->
		<div class="flex shrink-0 flex-col bg-[var(--color-bg-2)] {(treeOnly || (compact && !selectedFile)) ? 'flex-1' : 'w-[380px] border-r border-[var(--color-border)]'}"
		>

			<!-- Path input -->
			<form onsubmit={handlePathSubmit} class="border-b border-[var(--color-border)] px-4 py-3">
				<div class="flex items-center gap-2 rounded-[var(--radius-input)] border px-3 py-1.5 transition-colors duration-150
					{pathInputFocused
						? 'border-[var(--color-accent)]/50 bg-[var(--color-bg-0)]'
						: 'border-[var(--color-border)] bg-[var(--color-bg-1)]'}">
					<!-- Terminal prompt icon -->
					<span class="shrink-0 font-mono text-badge text-[var(--color-text-muted)] select-none" aria-hidden="true">
						$
					</span>
					<input
						type="text"
						bind:this={pathInputEl}
						bind:value={pathInput}
						onfocus={() => (pathInputFocused = true)}
						onblur={() => (pathInputFocused = false)}
						onkeydown={handleKeydown}
						placeholder="Enter path..."
						spellcheck="false"
						autocomplete="off"
						class="flex-1 bg-transparent font-mono text-meta text-[var(--color-text-primary)] outline-none placeholder:text-[var(--color-text-muted)]"
					/>
					<button
						type="submit"
						class="shrink-0 flex items-center gap-1 rounded-[var(--radius-button)] px-2 py-0.5 text-badge font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)] transition-colors hover:bg-[var(--color-accent-glow-mid)] hover:text-[var(--color-accent-mid)]"
					>
						<svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
							<line x1="5" y1="12" x2="19" y2="12" />
							<polyline points="12 5 19 12 12 19" />
						</svg>
						Go
					</button>
				</div>
			</form>

			<!-- Breadcrumbs -->
			<div class="flex items-center gap-0.5 border-b border-[var(--color-border)] px-2 py-2 overflow-x-auto">
				<!-- Up button -->
				<button
					onclick={() => navigateTo(currentPath + '/..')}
					disabled={!canGoUp}
					title="Go to parent directory"
					class="shrink-0 flex items-center justify-center rounded-[3px] w-6 h-6 transition-colors
						{canGoUp
							? 'text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-4)] hover:text-[var(--color-text-primary)]'
							: 'text-[var(--color-text-muted)] opacity-30 cursor-not-allowed'}"
				>
					<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
						<path d="M15 18l-6-6 6-6" />
					</svg>
				</button>
				<span class="w-px h-4 bg-[var(--color-border)] shrink-0 mx-1"></span>
				{#each breadcrumbs() as crumb, i}
					{#if i > 0}
						<svg class="shrink-0 text-[var(--color-text-muted)]" width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
							<polyline points="9 18 15 12 9 6" />
						</svg>
					{/if}
					<button
						onclick={() => navigateTo(crumb.path)}
						class="shrink-0 rounded-[3px] px-1.5 py-0.5 font-mono text-label transition-colors hover:bg-[var(--color-bg-4)] hover:text-[var(--color-text-primary)]
							{i === breadcrumbs().length - 1
								? 'text-[var(--color-text-primary)]'
								: 'text-[var(--color-text-tertiary)]'}"
					>
						{#if i === 0}
							<!-- Root icon -->
							<svg class="inline -mt-px" width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
								<path d="M3 9l9-7 9 7v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" />
							</svg>
						{:else}
							{crumb.name}
						{/if}
					</button>
				{/each}
			</div>

			<!-- File list -->
			<div class="flex-1 overflow-y-auto">
				{#if dirLoading}
					<div class="flex items-center justify-center py-12">
						<div class="flex items-center gap-2 text-meta text-[var(--color-text-secondary)]">
							<svg class="animate-spin" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
								<path d="M21 12a9 9 0 1 1-6.219-8.56" />
							</svg>
							Loading...
						</div>
					</div>
				{:else if dirError}
					<div class="px-4 py-4">
						<div class="flex items-start gap-2.5 rounded-[var(--radius-card)] border border-[var(--color-red)]/25 bg-[var(--color-red)]/6 px-3.5 py-3">
							<svg class="mt-0.5 shrink-0 text-[var(--color-red)]" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
								<circle cx="12" cy="12" r="10" /><line x1="12" y1="8" x2="12" y2="12" /><line x1="12" y1="16" x2="12.01" y2="16" />
							</svg>
							<span class="text-meta text-[var(--color-red)]">{dirError}</span>
						</div>
					</div>
				{:else if entries.length === 0}
					<div class="flex flex-col items-center justify-center py-16 gap-3">
						<div class="flex h-10 w-10 items-center justify-center rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-3)]" style="animation: iconFloat 3s ease-in-out infinite">
							<svg class="text-[var(--color-text-muted)]" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
								<path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z" />
							</svg>
						</div>
						<span class="text-meta text-[var(--color-text-muted)]">Nothing here yet</span>
					</div>
				{:else}
					{#each sortedEntries as entry, idx (entry.path)}
						<button
							onclick={() => selectFile(entry)}
							class="file-row flex w-full items-center gap-3 px-4 py-[7px] text-left
								{selectedFile?.path === entry.path ? 'active' : ''}
								{idx < 30 ? 'row-enter' : ''}"
							style={idx < 30 ? `animation-delay: ${idx * 12}ms` : undefined}
						>
							<!-- Icon -->
							{#if fileIcon(entry) === 'dir'}
								<svg class="shrink-0 text-[var(--color-accent-mid)]" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
									<path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z" />
								</svg>
							{:else if fileIcon(entry) === 'link'}
								<svg class="shrink-0 text-[var(--color-blue)]" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
									<path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71" />
									<path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71" />
								</svg>
							{:else if fileIcon(entry) === 'special'}
								<svg class="shrink-0 text-[var(--color-text-muted)]" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
									<circle cx="12" cy="12" r="10" />
									<line x1="4.93" y1="4.93" x2="19.07" y2="19.07" />
								</svg>
							{:else}
								<svg class="shrink-0" style="color: {extColor(entry.name)}" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
									<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z" />
									<polyline points="14 2 14 8 20 8" />
								</svg>
							{/if}

							<!-- Name + metadata -->
							<div class="flex flex-1 items-center gap-2 overflow-hidden">
								<span class="truncate font-mono text-meta
									{entry.type === 'directory'
										? 'text-[var(--color-text-primary)] font-medium'
										: 'text-[var(--color-text-secondary)]'}">
									{entry.name}
								</span>
								{#if entry.type === 'symlink' && entry.symlink_target}
									<span class="truncate font-mono text-badge text-[var(--color-text-muted)]">
										&rarr; {entry.symlink_target}
									</span>
								{/if}
							</div>

							<!-- Size + extension hint (files only) -->
							{#if entry.type === 'file'}
								{#if fileExt(entry.name)}
									<span class="shrink-0 font-mono text-[9px] uppercase tracking-[0.05em]" style="color: {extColor(entry.name)}; opacity: 0.7">
										{fileExt(entry.name)}
									</span>
								{/if}
								<span class="shrink-0 font-mono text-badge text-[var(--color-text-muted)]">
									{formatFileSize(entry.size)}
								</span>
							{/if}

							<!-- Permissions -->
							<span class="hidden shrink-0 font-mono text-badge text-[var(--color-text-muted)] xl:inline">
								{entry.permissions}
							</span>
						</button>
					{/each}
				{/if}
			</div>

			<!-- Footer: entry count -->
			{#if !dirLoading && !dirError && entries.length > 0}
				<div class="border-t border-[var(--color-border)] px-4 py-2 flex items-center gap-3">
					{#if dirCount > 0}
						<span class="font-mono text-badge text-[var(--color-text-muted)]">
							{dirCount} dir{dirCount !== 1 ? 's' : ''}
						</span>
					{/if}
					{#if fileCount > 0}
						<span class="font-mono text-badge text-[var(--color-text-muted)]">
							{fileCount} file{fileCount !== 1 ? 's' : ''}
						</span>
					{/if}
				</div>
			{/if}
		</div>

		<!-- Right panel: File preview -->
		{#if !treeOnly && (!compact || selectedFile)}
		<div class="flex flex-1 flex-col min-w-0 bg-[var(--color-bg-1)]">
			{#if !selectedFile}
				<!-- Empty state -->
				<div class="flex flex-1 items-center justify-center">
					<div class="flex flex-col items-center gap-3 text-center">
						<div class="flex h-12 w-12 items-center justify-center rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)]" style="animation: iconFloat 3s ease-in-out infinite">
							<svg class="text-[var(--color-text-muted)]" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
								<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z" />
								<polyline points="14 2 14 8 20 8" />
							</svg>
						</div>
						<div class="flex flex-col gap-1">
							<span class="text-ui text-[var(--color-text-secondary)]">No file selected</span>
							<span class="text-meta text-[var(--color-text-muted)]">Choose a file from the tree, or enter a path directly</span>
						</div>
					</div>
				</div>
			{:else}
				<!-- File header -->
				<div class="flex items-center justify-between border-b border-[var(--color-border)] bg-[var(--color-bg-2)] px-5 py-2.5">
					<div class="flex items-center gap-2.5 overflow-hidden">
						{#if isBinaryFile(selectedFile.name) || isFileTooLarge(selectedFile.size)}
							<svg class="shrink-0 text-[var(--color-amber)]" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
								<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z" />
								<polyline points="14 2 14 8 20 8" />
							</svg>
						{:else}
							<svg class="shrink-0" style="color: {extColor(selectedFile.name)}" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
								<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z" />
								<polyline points="14 2 14 8 20 8" />
							</svg>
						{/if}
						<span class="truncate font-mono text-meta text-[var(--color-text-primary)]">{selectedFile.path}</span>
						{#if extLabel(selectedFile.name)}
							<span
								class="shrink-0 rounded-[3px] border px-1.5 py-0.5 font-mono text-badge font-semibold uppercase tracking-[0.03em]"
								style="color: {extColor(selectedFile.name)}; border-color: color-mix(in srgb, {extColor(selectedFile.name)} 25%, transparent); background: color-mix(in srgb, {extColor(selectedFile.name)} 8%, transparent)"
							>
								{extLabel(selectedFile.name)}
							</span>
						{/if}
					</div>
					<div class="flex items-center gap-3 shrink-0 ml-4">
						<span class="font-mono text-badge text-[var(--color-text-muted)]">{formatFileSize(selectedFile.size)}</span>
						<button
							onclick={handleDownload}
							disabled={downloading || !isDownloadable}
							title={isDownloadable ? undefined : 'Only regular files can be downloaded'}
							class="flex items-center gap-1.5 rounded-[var(--radius-button)] border border-[var(--color-border)] bg-[var(--color-bg-3)] px-2.5 py-1 text-badge font-semibold uppercase tracking-[0.05em] text-[var(--color-text-secondary)] transition-colors hover:bg-[var(--color-bg-4)] hover:text-[var(--color-text-primary)] disabled:opacity-50 disabled:cursor-not-allowed"
						>
							{#if downloading}
								<svg class="animate-spin" width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><path d="M21 12a9 9 0 1 1-6.219-8.56" /></svg>
							{:else}
								<svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
									<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4" />
									<polyline points="7 10 12 15 17 10" />
									<line x1="12" y1="15" x2="12" y2="3" />
								</svg>
							{/if}
							Download
						</button>
					</div>
				</div>

				<!-- File content -->
				<div class="flex-1 overflow-auto">
					{#if fileLoading}
						<div class="flex items-center justify-center py-16">
							<div class="flex items-center gap-2 text-meta text-[var(--color-text-secondary)]">
								<svg class="animate-spin" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
									<path d="M21 12a9 9 0 1 1-6.219-8.56" />
								</svg>
								Reading file...
							</div>
						</div>
					{:else if fileError}
						<div class="px-5 py-5">
							<div class="flex items-start gap-2.5 rounded-[var(--radius-card)] border border-[var(--color-red)]/25 bg-[var(--color-red)]/6 px-3.5 py-3">
								<svg class="mt-0.5 shrink-0 text-[var(--color-red)]" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
									<circle cx="12" cy="12" r="10" /><line x1="12" y1="8" x2="12" y2="12" /><line x1="12" y1="16" x2="12.01" y2="16" />
								</svg>
								<span class="text-meta text-[var(--color-red)]">{fileError}</span>
							</div>
						</div>
					{:else if isSpecialFile}
						<!-- Device file, pipe, socket, etc. — can't read or download -->
						<div class="flex flex-1 items-center justify-center py-20">
							<div class="flex flex-col items-center gap-5 text-center" style="animation: fadeUp 0.25s ease both">
								<div class="flex h-14 w-14 items-center justify-center rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-3)]">
									<svg class="text-[var(--color-text-muted)]" width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
										<circle cx="12" cy="12" r="10" />
										<line x1="4.93" y1="4.93" x2="19.07" y2="19.07" />
									</svg>
								</div>
								<div class="flex flex-col gap-1.5">
									<span class="text-ui font-medium text-[var(--color-text-primary)]">Special file</span>
									<span class="text-meta text-[var(--color-text-tertiary)]">
										<code class="rounded bg-[var(--color-bg-4)] px-1.5 py-0.5 font-mono text-[var(--color-text-secondary)]">{selectedFile.name}</code>
										is a device, socket, or pipe
									</span>
									<span class="mt-1 text-meta text-[var(--color-text-muted)]">
										Special files can't be previewed or downloaded.
									</span>
								</div>
							</div>
						</div>
					{:else if !isDownloadable}
						<!-- Symlink — no preview or download -->
						<div class="flex flex-1 items-center justify-center py-20">
							<div class="flex flex-col items-center gap-5 text-center" style="animation: fadeUp 0.25s ease both">
								<div class="flex h-14 w-14 items-center justify-center rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-3)]">
									<svg class="text-[var(--color-blue)]" width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
										<path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71" />
										<path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71" />
									</svg>
								</div>
								<div class="flex flex-col gap-1.5">
									<span class="text-ui font-medium text-[var(--color-text-primary)]">Symlink</span>
									{#if selectedFile.symlink_target}
										<span class="text-meta text-[var(--color-text-tertiary)]">
											Points to <code class="rounded bg-[var(--color-bg-4)] px-1.5 py-0.5 font-mono text-[var(--color-text-secondary)]">{selectedFile.symlink_target}</code>
										</span>
									{/if}
									<span class="mt-1 text-meta text-[var(--color-text-muted)]">
										Symlinks can't be downloaded directly. Navigate to the target file instead.
									</span>
								</div>
							</div>
						</div>
					{:else if isBinaryFile(selectedFile.name) || isFileTooLarge(selectedFile.size) || (selectedFile && fileContent === null && !fileLoading)}
						<!-- Binary / too large / unreadable — download prompt -->
						<div class="flex flex-1 items-center justify-center py-20">
							<div class="flex flex-col items-center gap-5 text-center" style="animation: fadeUp 0.25s ease both">
								<div class="flex h-14 w-14 items-center justify-center rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-3)]">
									{#if isFileTooLarge(selectedFile.size)}
										<svg class="text-[var(--color-amber)]" width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
											<path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z" />
											<line x1="12" y1="9" x2="12" y2="13" />
											<line x1="12" y1="17" x2="12.01" y2="17" />
										</svg>
									{:else}
										<svg class="text-[var(--color-text-muted)]" width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
											<rect x="3" y="3" width="18" height="18" rx="2" ry="2" />
											<line x1="9" y1="3" x2="9" y2="21" />
										</svg>
									{/if}
								</div>
								<div class="flex flex-col gap-1.5">
									{#if isFileTooLarge(selectedFile.size)}
										<span class="text-ui font-medium text-[var(--color-text-primary)]">Too large to preview</span>
										<span class="text-meta text-[var(--color-text-tertiary)]">
											{formatFileSize(selectedFile.size)} — preview limit is 10 MB
										</span>
									{:else}
										<span class="text-ui font-medium text-[var(--color-text-primary)]">Binary file</span>
										<span class="text-meta text-[var(--color-text-tertiary)]">
											Can't display as text — download to view
										</span>
									{/if}
								</div>
								<button
									onclick={handleDownload}
									class="mt-1 flex items-center gap-2 rounded-[var(--radius-button)] border border-[var(--color-accent)]/30 bg-[var(--color-accent-glow-mid)] px-4 py-2 text-meta font-semibold text-[var(--color-accent-bright)] transition-all duration-150 hover:border-[var(--color-accent)]/50 hover:bg-[var(--color-accent)]/15 hover:-translate-y-px active:translate-y-0"
								>
									<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
										<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4" />
										<polyline points="7 10 12 15 17 10" />
										<line x1="12" y1="15" x2="12" y2="3" />
									</svg>
									Download file
								</button>
							</div>
						</div>
					{:else if fileContent !== null}
						<!-- Text preview with line numbers (capped at MAX_PREVIEW_LINES) -->
						<div style="animation: fadeUp 0.15s ease both">
							<pre class="preview-code p-0 m-0"><code class="block">{#each previewLines.lines as line, i}<div class="code-line flex"><span class="line-num sticky left-0 inline-block w-[52px] shrink-0 select-none border-r border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 py-0 text-right font-mono text-badge leading-[1.65rem] text-[var(--color-text-muted)]">{i + 1}</span><span class="line-content flex-1 whitespace-pre-wrap break-all px-4 py-0 font-mono text-meta leading-[1.65rem]">{#if highlightedTokens && highlightedTokens[i]}{#each highlightedTokens[i] as token}<span style="color: {token.color ?? 'var(--color-text-secondary)'}">{token.content}</span>{/each}{:else}<span class="text-[var(--color-text-secondary)]">{line || ' '}</span>{/if}</span></div>{/each}</code></pre>
						</div>
						{#if previewLines.truncated}
							<div class="flex items-center justify-center gap-2 border-t border-[var(--color-border)] bg-[var(--color-bg-2)] px-4 py-3">
								<span class="text-meta text-[var(--color-text-tertiary)]">
									Showing {MAX_PREVIEW_LINES.toLocaleString()} of {previewLines.totalLines.toLocaleString()} lines
								</span>
								<button
									onclick={handleDownload}
									class="font-mono text-meta text-[var(--color-accent-mid)] transition-colors hover:text-[var(--color-accent-bright)]"
								>Download full file</button>
							</div>
						{/if}
					{/if}
				</div>
			{/if}
		</div>
		{/if}

	</div>
{/if}
