<script lang="ts">
	import {
		listDir,
		readFile,
		downloadFile,
		isBinaryFile,
		isFileTooLarge,
		formatFileSize,
		type FileEntry,
	} from '$lib/api/files';

	type Props = {
		sandboxId: string;
		isRunning: boolean;
	};

	let { sandboxId, isRunning }: Props = $props();

	// Directory navigation state
	let currentPath = $state('/');
	let entries = $state<FileEntry[]>([]);
	let dirLoading = $state(false);
	let dirError = $state<string | null>(null);

	// File preview state
	let selectedFile = $state<FileEntry | null>(null);
	let fileContent = $state<string | null>(null);
	let fileLoading = $state(false);
	let fileError = $state<string | null>(null);

	// Path input
	let pathInput = $state('/');
	let pathInputFocused = $state(false);

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

	async function navigateTo(path: string) {
		currentPath = normalizePath(path);
		pathInput = currentPath;
		selectedFile = null;
		fileContent = null;
		fileError = null;
		await loadDir();
	}

	function normalizePath(p: string): string {
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

	async function loadDir() {
		if (!isRunning) return;
		dirLoading = true;
		dirError = null;
		const result = await listDir(sandboxId, currentPath);
		if (result.ok) {
			entries = result.data.entries ?? [];
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

		selectedFile = entry;
		fileContent = null;
		fileError = null;

		// Check if we should preview or prompt download
		if (isBinaryFile(entry.name) || isFileTooLarge(entry.size)) {
			// Don't load content — the preview pane will show download prompt
			return;
		}

		fileLoading = true;
		const result = await readFile(sandboxId, entry.path);
		if (result.ok) {
			// Check if content appears to be binary (contains null bytes or mostly non-printable)
			if (looksLikeBinary(result.data)) {
				fileContent = null;
				// Will show download prompt
			} else {
				fileContent = result.data;
			}
		} else {
			fileError = result.error;
		}
		fileLoading = false;
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
		if (!selectedFile) return;
		try {
			await downloadFile(sandboxId, selectedFile.path, selectedFile.name);
		} catch {
			fileError = 'Download failed';
		}
	}

	function handlePathSubmit(e: SubmitEvent) {
		e.preventDefault();
		const target = pathInput.trim();
		if (!target) return;
		// If ends with / or has no extension, treat as directory navigation
		// Otherwise, attempt to open as a file
		const resolved = normalizePath(target);
		// Try to navigate — if it fails we'll show an error
		navigateOrOpenFile(resolved);
	}

	async function navigateOrOpenFile(path: string) {
		// First try as directory
		const dirResult = await listDir(sandboxId, path);
		if (dirResult.ok) {
			currentPath = path;
			pathInput = path;
			entries = dirResult.data.entries ?? [];
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
		const parentResult = await listDir(sandboxId, parentPath);
		if (parentResult.ok) {
			entries = parentResult.data.entries ?? [];
			// Find the file in parent listing
			const found = entries.find((e) => e.name === fileName);
			if (found && found.type !== 'directory') {
				await selectFile(found);
			} else {
				// Not found in parent either — show error
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
		return 'file';
	}

	function fmtModified(ts: number): string {
		if (!ts) return '—';
		return new Date(ts * 1000).toLocaleString([], {
			month: 'short',
			day: 'numeric',
			hour: '2-digit',
			minute: '2-digit',
		});
	}

	// Load initial directory on mount
	$effect(() => {
		if (isRunning) {
			loadDir();
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
		background-color: rgba(94, 140, 88, 0.08);
	}

	.preview-code {
		tab-size: 4;
		-moz-tab-size: 4;
	}

	/* Thin scrollbar for file tree and preview */
	.thin-scroll::-webkit-scrollbar { width: 6px; height: 6px; }
	.thin-scroll::-webkit-scrollbar-track { background: transparent; }
	.thin-scroll::-webkit-scrollbar-thumb {
		background: var(--color-bg-5);
		border-radius: 3px;
	}
	.thin-scroll::-webkit-scrollbar-thumb:hover {
		background: var(--color-text-muted);
	}

	@keyframes fadeIn {
		from { opacity: 0; }
		to   { opacity: 1; }
	}
	.fade-in {
		animation: fadeIn 0.2s ease both;
	}
</style>

{#if !isRunning}
	<div class="flex items-center gap-3 rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] px-5 py-4 m-8">
		<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="var(--color-text-muted)" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
			<path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z" />
		</svg>
		<span class="text-ui text-[var(--color-text-tertiary)]">
			File browser is only available for running capsules.
		</span>
	</div>
{:else}
	<div class="flex flex-1 min-h-0">

		<!-- Left panel: File tree -->
		<div class="flex w-[380px] shrink-0 flex-col border-r border-[var(--color-border)]">

			<!-- Path input -->
			<form onsubmit={handlePathSubmit} class="border-b border-[var(--color-border)] px-4 py-3">
				<div class="flex items-center gap-2 rounded-[var(--radius-input)] border px-3 py-1.5 transition-colors duration-150
					{pathInputFocused
						? 'border-[var(--color-accent)]/50 bg-[var(--color-bg-1)]'
						: 'border-[var(--color-border)] bg-[var(--color-bg-2)]'}">
					<svg class="shrink-0 text-[var(--color-text-muted)]" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
						<polyline points="16 3 21 3 21 8" />
						<line x1="4" y1="20" x2="21" y2="3" />
						<polyline points="21 16 21 21 16 21" />
						<line x1="15" y1="15" x2="21" y2="21" />
						<line x1="4" y1="4" x2="9" y2="9" />
					</svg>
					<input
						type="text"
						bind:value={pathInput}
						onfocus={() => (pathInputFocused = true)}
						onblur={() => (pathInputFocused = false)}
						onkeydown={handleKeydown}
						placeholder="/path/to/file"
						spellcheck="false"
						autocomplete="off"
						class="flex-1 bg-transparent font-mono text-meta text-[var(--color-text-primary)] outline-none placeholder:text-[var(--color-text-muted)]"
					/>
					<button
						type="submit"
						class="shrink-0 rounded-[3px] px-1.5 py-0.5 text-badge font-semibold uppercase tracking-[0.05em] text-[var(--color-text-muted)] transition-colors hover:bg-[var(--color-bg-4)] hover:text-[var(--color-text-secondary)]"
					>
						Go
					</button>
				</div>
			</form>

			<!-- Breadcrumbs -->
			<div class="flex items-center gap-1 border-b border-[var(--color-border)] px-4 py-2 overflow-x-auto">
				{#each breadcrumbs() as crumb, i}
					{#if i > 0}
						<svg class="shrink-0 text-[var(--color-text-muted)]" width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
							<polyline points="9 18 15 12 9 6" />
						</svg>
					{/if}
					<button
						onclick={() => navigateTo(crumb.path)}
						class="shrink-0 rounded-[3px] px-1.5 py-0.5 font-mono text-label text-[var(--color-text-secondary)] transition-colors hover:bg-[var(--color-bg-4)] hover:text-[var(--color-text-primary)]
							{i === breadcrumbs().length - 1 ? 'text-[var(--color-text-primary)]' : ''}"
					>
						{crumb.name}
					</button>
				{/each}
			</div>

			<!-- File list -->
			<div class="thin-scroll flex-1 overflow-y-auto">
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
					<div class="flex flex-col items-center justify-center py-12 gap-2">
						<svg class="text-[var(--color-text-muted)]" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
							<path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z" />
						</svg>
						<span class="text-meta text-[var(--color-text-muted)]">Empty directory</span>
					</div>
				{:else}
					<!-- Parent directory -->
					{#if currentPath !== '/'}
						<button
							onclick={() => navigateTo(currentPath + '/..')}
							class="file-row flex w-full items-center gap-3 px-4 py-2 text-left"
						>
							<svg class="shrink-0 text-[var(--color-text-muted)]" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
								<polyline points="15 18 9 12 15 6" />
							</svg>
							<span class="font-mono text-meta text-[var(--color-text-secondary)]">..</span>
						</button>
					{/if}

					{#each sortedEntries as entry (entry.path)}
						<button
							onclick={() => selectFile(entry)}
							class="file-row flex w-full items-center gap-3 px-4 py-[7px] text-left
								{selectedFile?.path === entry.path ? 'active' : ''}"
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
							{:else}
								<svg class="shrink-0 text-[var(--color-text-muted)]" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
									<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z" />
									<polyline points="14 2 14 8 20 8" />
								</svg>
							{/if}

							<!-- Name + metadata -->
							<div class="flex flex-1 items-center gap-2 overflow-hidden">
								<span class="truncate font-mono text-meta
									{entry.type === 'directory'
										? 'text-[var(--color-text-primary)]'
										: 'text-[var(--color-text-secondary)]'}">
									{entry.name}
								</span>
								{#if entry.type === 'symlink' && entry.symlink_target}
									<span class="truncate font-mono text-badge text-[var(--color-text-muted)]">
										&rarr; {entry.symlink_target}
									</span>
								{/if}
							</div>

							<!-- Size (files only) -->
							{#if entry.type === 'file'}
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
			{#if !dirLoading && !dirError}
				<div class="border-t border-[var(--color-border)] px-4 py-2">
					<span class="font-mono text-badge text-[var(--color-text-muted)]">
						{entries.length} item{entries.length !== 1 ? 's' : ''}
					</span>
				</div>
			{/if}
		</div>

		<!-- Right panel: File preview -->
		<div class="flex flex-1 flex-col min-w-0 bg-[var(--color-bg-1)]">
			{#if !selectedFile}
				<!-- Empty state -->
				<div class="flex flex-1 items-center justify-center">
					<div class="flex flex-col items-center gap-3 text-center">
						<svg class="text-[var(--color-text-muted)]" width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.2" stroke-linecap="round" stroke-linejoin="round" style="opacity: 0.5">
							<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z" />
							<polyline points="14 2 14 8 20 8" />
						</svg>
						<span class="text-meta text-[var(--color-text-muted)]">Select a file to preview</span>
					</div>
				</div>
			{:else}
				<!-- File header -->
				<div class="flex items-center justify-between border-b border-[var(--color-border)] px-5 py-3">
					<div class="flex items-center gap-2.5 overflow-hidden">
						<svg class="shrink-0 text-[var(--color-text-muted)]" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
							<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z" />
							<polyline points="14 2 14 8 20 8" />
						</svg>
						<span class="truncate font-mono text-meta text-[var(--color-text-primary)]">{selectedFile.path}</span>
					</div>
					<div class="flex items-center gap-3 shrink-0 ml-3">
						<span class="font-mono text-badge text-[var(--color-text-muted)]">{formatFileSize(selectedFile.size)}</span>
						<button
							onclick={handleDownload}
							class="flex items-center gap-1.5 rounded-[var(--radius-button)] border border-[var(--color-border)] bg-[var(--color-bg-3)] px-2.5 py-1 text-badge font-semibold uppercase tracking-[0.05em] text-[var(--color-text-secondary)] transition-colors hover:bg-[var(--color-bg-4)] hover:text-[var(--color-text-primary)]"
						>
							<svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
								<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4" />
								<polyline points="7 10 12 15 17 10" />
								<line x1="12" y1="15" x2="12" y2="3" />
							</svg>
							Download
						</button>
					</div>
				</div>

				<!-- File content -->
				<div class="thin-scroll flex-1 overflow-auto">
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
					{:else if isBinaryFile(selectedFile.name) || isFileTooLarge(selectedFile.size) || (selectedFile && fileContent === null && !fileLoading)}
						<!-- Binary / too large / unreadable — download prompt -->
						<div class="flex flex-1 items-center justify-center py-16">
							<div class="fade-in flex flex-col items-center gap-4 text-center">
								<div class="flex h-12 w-12 items-center justify-center rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-3)]">
									{#if isFileTooLarge(selectedFile.size)}
										<svg class="text-[var(--color-amber)]" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
											<path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z" />
											<line x1="12" y1="9" x2="12" y2="13" />
											<line x1="12" y1="17" x2="12.01" y2="17" />
										</svg>
									{:else}
										<svg class="text-[var(--color-text-muted)]" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
											<rect x="3" y="3" width="18" height="18" rx="2" ry="2" />
											<line x1="9" y1="3" x2="9" y2="21" />
										</svg>
									{/if}
								</div>
								<div class="flex flex-col gap-1.5">
									{#if isFileTooLarge(selectedFile.size)}
										<span class="text-ui font-medium text-[var(--color-text-primary)]">File too large to preview</span>
										<span class="text-meta text-[var(--color-text-tertiary)]">
											{formatFileSize(selectedFile.size)} exceeds the 10 MB preview limit
										</span>
									{:else}
										<span class="text-ui font-medium text-[var(--color-text-primary)]">Binary file</span>
										<span class="text-meta text-[var(--color-text-tertiary)]">
											This file cannot be displayed as text
										</span>
									{/if}
								</div>
								<button
									onclick={handleDownload}
									class="mt-1 flex items-center gap-2 rounded-[var(--radius-button)] border border-[var(--color-accent)]/30 bg-[var(--color-accent-glow-mid)] px-4 py-2 text-meta font-semibold text-[var(--color-accent-bright)] transition-colors hover:border-[var(--color-accent)]/50 hover:bg-[var(--color-accent-glow-mid)]"
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
						<!-- Text preview with line numbers -->
						<div class="fade-in">
							<pre class="preview-code p-0 m-0"><code class="block">{#each fileContent.split('\n') as line, i}<div class="flex hover:bg-[var(--color-bg-2)]"><span class="sticky left-0 inline-block w-[52px] shrink-0 select-none border-r border-[var(--color-border)] bg-[var(--color-bg-1)] px-3 py-0 text-right font-mono text-badge leading-[1.65rem] text-[var(--color-text-muted)]">{i + 1}</span><span class="flex-1 whitespace-pre-wrap break-all px-4 py-0 font-mono text-meta leading-[1.65rem] text-[var(--color-text-secondary)]">{line}</span></div>{/each}</code></pre>
						</div>
					{/if}
				</div>
			{/if}
		</div>

	</div>
{/if}
