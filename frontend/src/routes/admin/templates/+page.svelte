<script lang="ts">
	import AdminSidebar from '$lib/components/AdminSidebar.svelte';
	import CopyButton from '$lib/components/CopyButton.svelte';
	import { onMount, onDestroy } from 'svelte';
	import { toast } from '$lib/toast.svelte';
	import { formatDate, timeAgo } from '$lib/utils/format';
	import {
		listBuilds,
		createBuild,
		cancelBuild,
		listAdminTemplates,
		deleteAdminTemplate,
		type Build,
		type BuildLogEntry,
		type AdminTemplate
	} from '$lib/api/builds';

	let collapsed = $state(
		typeof window !== 'undefined'
			? localStorage.getItem('wrenn_sidebar_collapsed') === 'true'
			: false
	);

	let activeTab = $state<'templates' | 'builds'>('templates');

	// Templates state
	let templates = $state<AdminTemplate[]>([]);
	let templatesLoading = $state(true);
	let templatesError = $state<string | null>(null);

	// Builds state
	let builds = $state<Build[]>([]);
	let buildsLoading = $state(true);
	let buildsError = $state<string | null>(null);

	// Polling
	let pollInterval: ReturnType<typeof setInterval> | null = null;
	let hasActiveBuilds = $derived(builds.some((b) => b.status === 'pending' || b.status === 'running'));
	let visibilityHandler: (() => void) | null = null;

	// Build log expansion
	let expandedBuildId = $state<string | null>(null);
	let expandedSteps = $state<Set<number>>(new Set());

	// Delete template state
	let deleteTarget = $state<AdminTemplate | null>(null);
	let deleting = $state(false);
	let deleteError = $state<string | null>(null);

	// Create dialog state
	let showCreate = $state(false);
	let createForm = $state({
		name: '',
		base_template: 'minimal',
		vcpus: 1,
		memory_mb: 512,
		recipe: '',
		healthcheck: '',
		skip_pre_post: false,
		archive: null as File | null
	});
	let creating = $state(false);
	let createError = $state<string | null>(null);

	// Cancel build state
	let cancelingBuildId = $state<string | null>(null);

	// Stats
	let templateCount = $derived(templates.length);
	let snapshotCount = $derived(templates.filter((t) => t.type === 'snapshot').length);
	let baseCount = $derived(templates.filter((t) => t.type === 'base').length);
	let runningBuilds = $derived(builds.filter((b) => b.status === 'running').length);

	async function fetchTemplates() {
		templatesLoading = true;
		templatesError = null;
		const result = await listAdminTemplates();
		if (result.ok) {
			templates = result.data;
		} else {
			templatesError = result.error;
		}
		templatesLoading = false;
	}

	async function fetchBuilds() {
		const wasFirst = buildsLoading;
		if (wasFirst) buildsLoading = true;
		buildsError = null;
		const result = await listBuilds();
		if (result.ok) {
			builds = result.data;
		} else {
			buildsError = result.error;
		}
		if (wasFirst) buildsLoading = false;
	}

	function startPolling() {
		stopPolling();
		pollInterval = setInterval(() => {
			if (hasActiveBuilds && activeTab === 'builds') fetchBuilds();
		}, 3000);
	}

	function stopPolling() {
		if (pollInterval) {
			clearInterval(pollInterval);
			pollInterval = null;
		}
	}

	async function handleCreate() {
		creating = true;
		createError = null;

		const lines = createForm.recipe
			.split('\n')
			.map((l) => l.trim())
			.filter((l) => l.length > 0);

		if (lines.length === 0) {
			createError = 'Recipe must contain at least one command.';
			creating = false;
			return;
		}

		const result = await createBuild({
			name: createForm.name.trim(),
			base_template: createForm.base_template.trim() || 'minimal',
			recipe: lines,
			healthcheck: createForm.healthcheck.trim() || undefined,
			vcpus: createForm.vcpus,
			memory_mb: createForm.memory_mb,
			skip_pre_post: createForm.skip_pre_post,
			archive: createForm.archive || undefined
		});

		if (result.ok) {
			showCreate = false;
			createForm = { name: '', base_template: 'minimal', vcpus: 1, memory_mb: 512, recipe: '', healthcheck: '', skip_pre_post: false, archive: null };
			builds = [result.data, ...builds];
			activeTab = 'builds';
			expandedBuildId = result.data.id;
			toast.success('Build queued');
			startPolling();
		} else {
			createError = result.error;
		}
		creating = false;
	}

	async function handleDeleteTemplate() {
		if (!deleteTarget) return;
		deleting = true;
		deleteError = null;
		const name = deleteTarget.name;
		const result = await deleteAdminTemplate(name);
		if (result.ok) {
			templates = templates.filter((t) => t.name !== name);
			deleteTarget = null;
			toast.success('Template deleted');
		} else {
			deleteError = result.error;
		}
		deleting = false;
	}

	async function handleCancelBuild(buildId: string) {
		cancelingBuildId = buildId;
		const result = await cancelBuild(buildId);
		if (result.ok) {
			builds = builds.map((b) => b.id === buildId ? { ...b, status: 'cancelled' } : b);
			toast.success('Build cancelled');
		} else {
			toast.error(result.error ?? 'Failed to cancel build');
		}
		cancelingBuildId = null;
	}

	function toggleBuildExpand(buildId: string) {
		if (expandedBuildId === buildId) {
			expandedBuildId = null;
			expandedSteps = new Set();
		} else {
			expandedBuildId = buildId;
			expandedSteps = new Set();
		}
	}

	function toggleStepExpand(step: number) {
		const next = new Set(expandedSteps);
		if (next.has(step)) {
			next.delete(step);
		} else {
			next.add(step);
		}
		expandedSteps = next;
	}

	function formatBytes(bytes: number): string {
		if (bytes === 0) return '0 B';
		const k = 1024;
		const sizes = ['B', 'KB', 'MB', 'GB'];
		const i = Math.floor(Math.log(bytes) / Math.log(k));
		return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i];
	}

	function formatDuration(startedAt?: string, completedAt?: string): string {
		if (!startedAt) return '—';
		const start = new Date(startedAt).getTime();
		const end = completedAt ? new Date(completedAt).getTime() : Date.now();
		const sec = Math.round((end - start) / 1000);
		if (sec < 60) return `${sec}s`;
		return `${Math.floor(sec / 60)}m ${sec % 60}s`;
	}

	function statusColor(status: string): string {
		switch (status) {
			case 'success': return 'var(--color-accent-bright)';
			case 'failed': return 'var(--color-red)';
			case 'running': return 'var(--color-blue)';
			case 'cancelled': return 'var(--color-amber)';
			default: return 'var(--color-text-muted)';
		}
	}

	// Returns [keyword, rest] from a recipe instruction string.
	function splitInstruction(cmd: string): [string, string] {
		const idx = cmd.indexOf(' ');
		if (idx === -1) return [cmd.toUpperCase(), ''];
		return [cmd.slice(0, idx).toUpperCase(), cmd.slice(idx + 1)];
	}

	function keywordColor(keyword: string): string {
		switch (keyword) {
			case 'RUN':     return 'var(--color-blue)';
			case 'START':   return 'var(--color-accent-bright)';
			case 'ENV':     return 'var(--color-amber)';
			case 'USER':    return 'var(--color-accent)';
			case 'COPY':    return 'var(--color-text-bright)';
			case 'WORKDIR': return 'var(--color-text-tertiary)';
			default:        return 'var(--color-text-muted)';
		}
	}

	onMount(() => {
		fetchTemplates();
		fetchBuilds().then(startPolling);

		// Pause polling when the browser tab is hidden.
		visibilityHandler = () => {
			if (document.hidden) {
				stopPolling();
			} else {
				startPolling();
			}
		};
		document.addEventListener('visibilitychange', visibilityHandler);
	});

	onDestroy(() => {
		stopPolling();
		if (visibilityHandler) document.removeEventListener('visibilitychange', visibilityHandler);
	});
</script>

<div class="flex h-screen overflow-hidden bg-[var(--color-bg-0)]">
	<AdminSidebar bind:collapsed />

	<main class="flex min-w-0 flex-1 flex-col overflow-hidden">
		<!-- Header -->
		<header class="flex shrink-0 flex-col gap-4 border-b border-[var(--color-border)] bg-[var(--color-bg-1)] px-6 py-5">
			<div class="flex items-start justify-between">
				<div>
					<h1 class="font-serif text-[1.75rem] leading-none tracking-[-0.03em] text-[var(--color-text-bright)]">
						Templates
					</h1>
					<p class="mt-1.5 text-ui text-[var(--color-text-tertiary)]">
						Build and manage global templates available to all teams.
					</p>
				</div>
				<button
					onclick={() => { showCreate = true; createError = null; createForm = { name: '', base_template: 'minimal', vcpus: 1, memory_mb: 512, recipe: '', healthcheck: '', skip_pre_post: false, archive: null }; }}
					class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-accent)] px-4 py-2 text-ui font-semibold text-white shadow-sm transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0"
				>
					<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>
					Create Template
				</button>
			</div>

			<!-- Stat pills -->
			{#if !templatesLoading && !templatesError}
				<div class="flex items-center gap-2">
					<div class="flex items-baseline gap-1 rounded-[var(--radius-button)] border border-[var(--color-border)] bg-[var(--color-bg-2)] px-2.5 py-1">
						<span class="font-mono font-semibold text-ui tabular-nums text-[var(--color-text-bright)]">{templateCount}</span>
						<span class="text-label text-[var(--color-text-muted)]">templates</span>
					</div>
					<div class="flex items-baseline gap-1 rounded-[var(--radius-button)] border border-[var(--color-border)] bg-[var(--color-bg-2)] px-2.5 py-1">
						<span class="font-mono font-semibold text-ui tabular-nums text-[var(--color-text-bright)]">{baseCount}</span>
						<span class="text-label text-[var(--color-text-muted)]">base</span>
					</div>
					<div class="flex items-baseline gap-1 rounded-[var(--radius-button)] border border-[var(--color-accent)]/25 bg-[var(--color-accent)]/8 px-2.5 py-1">
						<span class="font-mono font-semibold text-ui tabular-nums text-[var(--color-accent-bright)]">{snapshotCount}</span>
						<span class="text-label text-[var(--color-accent-bright)]/70">snapshots</span>
					</div>
					{#if runningBuilds > 0}
						<div class="flex items-baseline gap-1.5 rounded-[var(--radius-button)] border border-[var(--color-blue)]/25 bg-[var(--color-blue)]/8 px-2.5 py-1">
							<span class="relative mt-px flex h-1.5 w-1.5 shrink-0 self-center">
								<span class="absolute inline-flex h-full w-full animate-ping rounded-full bg-[var(--color-blue)] opacity-60"></span>
								<span class="relative inline-flex h-1.5 w-1.5 rounded-full bg-[var(--color-blue)]"></span>
							</span>
							<span class="font-mono font-semibold text-ui tabular-nums text-[var(--color-blue)]">{runningBuilds}</span>
							<span class="text-label text-[var(--color-blue)]/70">building</span>
						</div>
					{/if}
				</div>
			{/if}
		</header>

		<!-- Tabs -->
		<div class="flex shrink-0 border-b border-[var(--color-border)] bg-[var(--color-bg-1)] px-6">
			{#each [['templates', 'Templates', templateCount], ['builds', 'Builds', builds.length]] as [id, label, count] (id)}
				<button
					onclick={() => { activeTab = id as 'templates' | 'builds'; }}
					class="relative py-3 pr-5 text-ui transition-colors duration-150 {activeTab === id
						? 'font-medium text-[var(--color-text-bright)]'
						: 'text-[var(--color-text-tertiary)] hover:text-[var(--color-text-secondary)]'}"
				>
					{label}
					{#if activeTab === id}
						<span class="absolute bottom-0 left-0 right-5 h-[2px] rounded-t-full bg-[var(--color-accent)]"></span>
					{/if}
					{#if !templatesLoading}
						<span class="ml-2 rounded-full bg-[var(--color-bg-4)] px-1.5 py-0.5 text-label text-[var(--color-text-muted)]">
							{count}
						</span>
					{/if}
				</button>
			{/each}
		</div>

		<!-- Body -->
		<div class="flex-1 overflow-y-auto p-6">
			{#if activeTab === 'templates'}
				{#if templatesLoading}
					{@render skeletonRows(5, ['Name', 'Type', 'Specs', 'Size', 'Created', ''])}
				{:else if templatesError}
					<div class="rounded-[var(--radius-card)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-4 py-3 text-ui text-[var(--color-red)]">
						{templatesError}
					</div>
				{:else if templates.length === 0}
					{@render emptyState('templates')}
				{:else}
					{@render templatesTable()}
				{/if}
			{:else}
				{#if buildsLoading}
					{@render skeletonRows(4, ['Build', 'Name', 'Status', 'Progress', 'Started', 'Duration'])}
				{:else if buildsError}
					<div class="rounded-[var(--radius-card)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-4 py-3 text-ui text-[var(--color-red)]">
						{buildsError}
					</div>
				{:else if builds.length === 0}
					{@render emptyState('builds')}
				{:else}
					{@render buildsTable()}
				{/if}
			{/if}
		</div>
	</main>
</div>

<!-- ── Snippets ─────────────────────────────────────────────────────── -->

{#snippet skeletonRows(count: number, headers: string[])}
	<div class="rounded-[var(--radius-card)] border border-[var(--color-border)] bg-[var(--color-bg-1)] overflow-hidden">
		<table class="w-full">
			<thead>
				<tr class="border-b border-[var(--color-border)]">
					{#each headers as h}
						<th class="px-4 py-3 text-left text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">{h}</th>
					{/each}
				</tr>
			</thead>
			<tbody>
				{#each Array(count) as _, i}
					<tr class="border-b border-[var(--color-border)] last:border-0" style="animation-delay: {i * 60}ms">
						{#each headers as _h, j}
							<td class="px-4 py-3.5">
								<div class="skeleton h-3 rounded" style="width: {60 + j * 12}px"></div>
							</td>
						{/each}
					</tr>
				{/each}
			</tbody>
		</table>
	</div>
{/snippet}

{#snippet emptyState(type: 'templates' | 'builds')}
	<div class="flex flex-col items-center justify-center py-24 text-center">
		<div class="mb-5 flex h-16 w-16 items-center justify-center rounded-[var(--radius-card)] border border-[var(--color-border)] bg-[var(--color-bg-2)]">
			{#if type === 'templates'}
				<svg width="28" height="28" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.25" stroke-linecap="round" stroke-linejoin="round" class="text-[var(--color-text-muted)]"><rect x="3" y="3" width="7" height="7"/><rect x="14" y="3" width="7" height="7"/><rect x="14" y="14" width="7" height="7"/><rect x="3" y="14" width="7" height="7"/></svg>
			{:else}
				<svg width="28" height="28" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.25" stroke-linecap="round" stroke-linejoin="round" class="text-[var(--color-text-muted)]"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8Z"/><path d="M14 2v6h6"/><path d="m16 13-3.5 3.5-2-2L8 17"/></svg>
			{/if}
		</div>
		<p class="font-serif text-[1.125rem] leading-snug text-[var(--color-text-secondary)]">
			{type === 'templates' ? 'No templates yet.' : 'No builds yet.'}
		</p>
		<p class="mt-1.5 text-ui text-[var(--color-text-muted)]">
			{type === 'templates'
				? 'Create a template to provide pre-configured environments for all teams.'
				: 'Start a template build to see progress and logs here.'}
		</p>
	</div>
{/snippet}

{#snippet templatesTable()}
	<div class="rounded-[var(--radius-card)] border border-[var(--color-border)] bg-[var(--color-bg-1)] overflow-hidden">
		<table class="w-full">
			<thead>
				<tr class="border-b border-[var(--color-border)]">
					<th class="px-4 py-3 text-left text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Name</th>
					<th class="px-4 py-3 text-left text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Type</th>
					<th class="hidden px-4 py-3 text-left text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)] md:table-cell">Specs</th>
					<th class="hidden px-4 py-3 text-left text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)] lg:table-cell">Size</th>
					<th class="hidden px-4 py-3 text-left text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)] lg:table-cell">Created</th>
					<th class="px-4 py-3"></th>
				</tr>
			</thead>
			<tbody>
				{#each templates as tmpl (tmpl.name)}
					<tr class="border-b border-[var(--color-border)] last:border-0 transition-colors duration-200 hover:bg-[var(--color-bg-2)]">
						<td class="px-4 py-3.5">
							<div class="flex items-center gap-1.5">
								<span class="font-mono text-meta text-[var(--color-text-primary)]">{tmpl.name}</span>
								<CopyButton value={tmpl.name} />
							</div>
						</td>
						<td class="px-4 py-3.5">
							{#if tmpl.type === 'snapshot'}
								<span class="inline-flex items-center rounded-full border border-[var(--color-accent)]/25 bg-[var(--color-accent)]/8 px-2 py-0.5 text-label font-medium text-[var(--color-accent-bright)]">
									snapshot
								</span>
							{:else}
								<span class="inline-flex items-center rounded-full border border-[var(--color-border)] bg-[var(--color-bg-3)] px-2 py-0.5 text-label font-medium text-[var(--color-text-secondary)]">
									base
								</span>
							{/if}
						</td>
						<td class="hidden px-4 py-3.5 md:table-cell">
							{#if tmpl.vcpus && tmpl.memory_mb}
								<span class="text-meta text-[var(--color-text-secondary)]">
									{tmpl.vcpus} vCPU · {tmpl.memory_mb} MB
								</span>
							{:else}
								<span class="text-meta text-[var(--color-text-muted)]">—</span>
							{/if}
						</td>
						<td class="hidden px-4 py-3.5 lg:table-cell">
							<span class="font-mono text-meta text-[var(--color-text-muted)]">
								{tmpl.size_bytes ? formatBytes(tmpl.size_bytes) : '—'}
							</span>
						</td>
						<td class="hidden px-4 py-3.5 lg:table-cell">
							<span class="text-meta text-[var(--color-text-muted)]" title={formatDate(tmpl.created_at)}>
								{timeAgo(tmpl.created_at)}
							</span>
						</td>
						<td class="px-4 py-3.5 text-right">
							<button
								onclick={() => { deleteTarget = tmpl; deleteError = null; }}
								class="rounded-[var(--radius-button)] px-3 py-1.5 text-meta text-[var(--color-text-tertiary)] transition-colors duration-150 hover:bg-[var(--color-red)]/10 hover:text-[var(--color-red)]"
							>
								Delete
							</button>
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
	</div>
{/snippet}

{#snippet buildsTable()}
	<div class="space-y-0 rounded-[var(--radius-card)] border border-[var(--color-border)] bg-[var(--color-bg-1)] overflow-hidden">
		<table class="w-full">
			<thead>
				<tr class="border-b border-[var(--color-border)]">
					<th class="px-4 py-3 text-left text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Build</th>
					<th class="px-4 py-3 text-left text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Name</th>
					<th class="hidden px-4 py-3 text-left text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)] md:table-cell">Base</th>
					<th class="px-4 py-3 text-left text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Status</th>
					<th class="hidden px-4 py-3 text-left text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)] md:table-cell">Progress</th>
					<th class="hidden px-4 py-3 text-left text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)] lg:table-cell">Started</th>
					<th class="hidden px-4 py-3 text-left text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)] lg:table-cell">Duration</th>
				</tr>
			</thead>
			<tbody>
				{#each builds as build (build.id)}
					<tr
						class="border-b border-[var(--color-border)] last:border-0 cursor-pointer transition-colors duration-200
							{expandedBuildId === build.id ? 'bg-[var(--color-bg-2)]' : 'hover:bg-[var(--color-bg-2)]'}"
						onclick={() => toggleBuildExpand(build.id)}
					>
						<td class="px-4 py-3.5">
							<div class="flex items-center gap-2">
								<svg
									width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor"
									stroke-width="2" stroke-linecap="round" stroke-linejoin="round"
									class="shrink-0 text-[var(--color-text-muted)] transition-transform duration-200 {expandedBuildId === build.id ? 'rotate-90' : ''}"
								>
									<polyline points="9 18 15 12 9 6"/>
								</svg>
								<span class="font-mono text-meta text-[var(--color-text-primary)]">{build.id}</span>
							</div>
						</td>
						<td class="px-4 py-3.5">
							<span class="text-meta text-[var(--color-text-primary)]">{build.name}</span>
						</td>
						<td class="hidden px-4 py-3.5 md:table-cell">
							<span class="font-mono text-meta text-[var(--color-text-muted)]">{build.base_template}</span>
						</td>
						<td class="px-4 py-3.5">
							<span class="flex items-center gap-1.5 text-meta font-medium" style="color: {statusColor(build.status)}">
								{#if build.status === 'running'}
									<span class="relative flex h-1.5 w-1.5 shrink-0">
										<span class="absolute inline-flex h-full w-full animate-ping rounded-full opacity-60" style="background: {statusColor(build.status)}"></span>
										<span class="relative inline-flex h-1.5 w-1.5 rounded-full" style="background: {statusColor(build.status)}"></span>
									</span>
								{:else if build.status === 'success'}
									<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"/></svg>
								{:else if build.status === 'failed'}
									<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
								{:else}
									<span class="h-1.5 w-1.5 shrink-0 rounded-full" style="background: {statusColor(build.status)}"></span>
								{/if}
								{build.status}
							</span>
						</td>
						<td class="hidden px-4 py-3.5 md:table-cell">
							<span class="font-mono text-meta text-[var(--color-text-muted)]">
								{build.current_step} / {build.total_steps}
							</span>
							{#if build.status === 'running' && build.total_steps > 0}
								<div class="mt-1.5 h-1 w-20 overflow-hidden rounded-full bg-[var(--color-bg-4)]">
									<div
										class="h-full rounded-full bg-[var(--color-blue)] transition-all duration-500"
										style="width: {(build.current_step / build.total_steps) * 100}%"
									></div>
								</div>
							{/if}
						</td>
						<td class="hidden px-4 py-3.5 lg:table-cell">
							<span class="text-meta text-[var(--color-text-muted)]" title={formatDate(build.started_at)}>
								{build.started_at ? timeAgo(build.started_at) : '—'}
							</span>
						</td>
						<td class="hidden px-4 py-3.5 lg:table-cell">
							<span class="font-mono text-meta text-[var(--color-text-muted)]">
								{formatDuration(build.started_at, build.completed_at)}
							</span>
						</td>
					</tr>
					<!-- Expanded build logs -->
					{#if expandedBuildId === build.id}
						<tr>
							<td colspan="7" class="border-b border-[var(--color-border)] last:border-0">
								<div class="bg-[var(--color-bg-0)] px-6 py-4" style="animation: fadeUp 0.15s ease both">
									{#if build.status === 'pending' || build.status === 'running'}
										<div class="mb-4 flex justify-end">
											<button
												onclick={(e) => { e.stopPropagation(); handleCancelBuild(build.id); }}
												disabled={cancelingBuildId === build.id}
												class="flex items-center gap-1.5 rounded-[var(--radius-button)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/8 px-3 py-1.5 text-meta text-[var(--color-red)] transition-colors duration-150 hover:bg-[var(--color-red)]/15 disabled:opacity-50"
											>
												{#if cancelingBuildId === build.id}
													<svg class="animate-spin" width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 12a9 9 0 1 1-6.219-8.56"/></svg>
												{:else}
													<svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
												{/if}
												Cancel build
											</button>
										</div>
									{/if}
									{#if build.error}
										<div class="mb-4 rounded-[var(--radius-input)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-3 py-2 text-meta text-[var(--color-red)]">
											{build.error}
										</div>
									{/if}

									{#if build.logs && build.logs.length > 0}
										<div class="space-y-1">
											{#each build.logs as log, i (i)}
												{@const isInternal = log.phase === 'pre-build' || log.phase === 'post-build'}
												{@const recipeIdx = log.phase === 'recipe' ? build.logs.filter(l => l.phase === 'recipe' && l.step <= log.step).length : 0}
												{@const phaseLabel = isInternal ? (log.phase === 'pre-build' ? 'Pre-build' : 'Post-build') : `Step ${recipeIdx}`}
												{@const [kw, kwRest] = splitInstruction(log.cmd)}
												<div class="rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-1)] overflow-hidden">
													<!-- Step header -->
													<button
														onclick={(e) => { e.stopPropagation(); toggleStepExpand(log.step); }}
														class="flex w-full items-center gap-3 px-3 py-2.5 text-left transition-colors duration-150 hover:bg-[var(--color-bg-2)]"
													>
														<!-- Status icon -->
														{#if log.ok}
															<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="var(--color-accent-bright)" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" class="shrink-0"><polyline points="20 6 9 17 4 12"/></svg>
														{:else}
															<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="var(--color-red)" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" class="shrink-0"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
														{/if}
														<span class="shrink-0 text-label font-semibold text-[var(--color-text-tertiary)]">{phaseLabel}</span>
														<code class="flex-1 truncate font-mono text-meta"><span style="color: {keywordColor(kw)}">{kw}</span>{#if kwRest}{' '}<span class="text-[var(--color-text-secondary)]">{kwRest}</span>{/if}</code>
														<span class="shrink-0 font-mono text-label text-[var(--color-text-muted)]">{log.elapsed_ms}ms</span>
														{#if log.exit !== 0}
															<span class="shrink-0 rounded-full bg-[var(--color-red)]/10 px-1.5 py-0.5 font-mono text-label text-[var(--color-red)]">
																exit {log.exit}
															</span>
														{/if}
														<svg
															width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor"
															stroke-width="2" stroke-linecap="round" stroke-linejoin="round"
															class="shrink-0 text-[var(--color-text-muted)] transition-transform duration-200 {expandedSteps.has(log.step) ? 'rotate-90' : ''}"
														>
															<polyline points="9 18 15 12 9 6"/>
														</svg>
													</button>

													<!-- Step output -->
													{#if expandedSteps.has(log.step)}
														<div class="border-t border-[var(--color-border)] bg-[var(--color-bg-0)] px-3 py-3" style="animation: fadeUp 0.12s ease both">
															{#if log.stdout}
																<div class="mb-2">
																	<span class="text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">stdout</span>
																	<pre class="mt-1 max-h-48 overflow-auto rounded-[var(--radius-input)] bg-[var(--color-bg-1)] px-3 py-2 font-mono text-meta leading-relaxed text-[var(--color-text-secondary)]">{log.stdout}</pre>
																</div>
															{/if}
															{#if log.stderr}
																<div>
																	<span class="text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">stderr</span>
																	<pre class="mt-1 max-h-48 overflow-auto rounded-[var(--radius-input)] bg-[var(--color-bg-1)] px-3 py-2 font-mono text-meta leading-relaxed text-[var(--color-red)]/80">{log.stderr}</pre>
																</div>
															{/if}
															{#if !log.stdout && !log.stderr}
																<span class="text-meta text-[var(--color-text-muted)]">No output</span>
															{/if}
														</div>
													{/if}
												</div>
											{/each}
										</div>
									{:else}
										<div class="flex items-center gap-2 text-meta text-[var(--color-text-muted)]">
											{#if build.status === 'pending' || build.status === 'running'}
												<svg class="animate-spin" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 12a9 9 0 1 1-6.219-8.56"/></svg>
												{build.status === 'pending' ? 'Waiting for worker…' : 'Running…'}
											{:else}
												No build logs recorded.
											{/if}
										</div>
									{/if}

									<!-- Recipe reference -->
									{#if build.recipe && build.recipe.length > 0}
										<div class="mt-4 border-t border-[var(--color-border)] pt-4">
											<div class="flex items-center gap-1.5">
												<span class="text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Recipe</span>
												<CopyButton value={build.recipe.join('\n')} />
											</div>
											<div class="mt-2 rounded-[var(--radius-input)] bg-[var(--color-bg-1)] border border-[var(--color-border)] px-3 py-2">
												{#each build.recipe as cmd, i}
													{@const [kw, kwRest] = splitInstruction(cmd)}
													<div class="flex gap-2 py-0.5">
														<span class="shrink-0 font-mono text-label text-[var(--color-text-muted)] tabular-nums">{i + 1}.</span>
														<code class="font-mono text-meta"><span style="color: {keywordColor(kw)}">{kw}</span>{#if kwRest}{' '}<span class="text-[var(--color-text-secondary)]">{kwRest}</span>{/if}</code>
													</div>
												{/each}
											</div>
										</div>
									{/if}

									{#if build.healthcheck}
										<div class="mt-3">
											<span class="text-label font-semibold uppercase tracking-[0.06em] text-[var(--color-text-tertiary)]">Healthcheck</span>
											<code class="ml-2 font-mono text-meta text-[var(--color-text-secondary)]">{build.healthcheck}</code>
										</div>
									{/if}
								</div>
							</td>
						</tr>
					{/if}
				{/each}
			</tbody>
		</table>
	</div>
{/snippet}

<!-- ── Create Template Dialog ──────────────────────────────────────── -->
{#if showCreate}
	<div class="fixed inset-0 z-50 flex items-center justify-center">
		<div
			class="absolute inset-0 bg-black/60"
			role="button"
			tabindex="-1"
			onclick={() => { if (!creating) showCreate = false; }}
			onkeydown={(e) => { if (e.key === 'Escape' && !creating) showCreate = false; }}
		></div>
		<div
			class="relative w-full max-w-[520px] max-h-[90vh] overflow-y-auto rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] p-6 shadow-xl"
			style="animation: fadeUp 0.18s cubic-bezier(0.25,1,0.5,1) both"
		>
			<h2 class="font-serif text-[1.375rem] leading-tight tracking-[-0.02em] text-[var(--color-text-bright)]">
				Create Template
			</h2>
			<p class="mt-1.5 text-ui text-[var(--color-text-tertiary)]">
				Build a new global template by running commands on a base image.
			</p>

			{#if createError}
				<div class="mt-4 rounded-[var(--radius-input)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-3 py-2 text-meta text-[var(--color-red)]">
					{createError}
				</div>
			{/if}

			<div class="mt-5 space-y-4">
				<div>
					<label class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]" for="tmpl-name">
						Template Name
					</label>
					<input
						id="tmpl-name"
						type="text"
						placeholder="e.g. python312, node20-full"
						bind:value={createForm.name}
						disabled={creating}
						class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-4)] px-3 py-2 text-ui text-[var(--color-text-bright)] outline-none placeholder:text-[var(--color-text-muted)] transition-colors duration-150 focus:border-[var(--color-accent)] disabled:opacity-60"
					/>
				</div>

				<div class="grid grid-cols-3 gap-3">
					<div>
						<label class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]" for="tmpl-base">
							Base
						</label>
						<input
							id="tmpl-base"
							type="text"
							bind:value={createForm.base_template}
							disabled={creating}
							class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-4)] px-3 py-2 text-ui text-[var(--color-text-bright)] outline-none placeholder:text-[var(--color-text-muted)] transition-colors duration-150 focus:border-[var(--color-accent)] disabled:opacity-60"
						/>
					</div>
					<div>
						<label class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]" for="tmpl-vcpus">
							vCPUs
						</label>
						<input
							id="tmpl-vcpus"
							type="number"
							min="1"
							bind:value={createForm.vcpus}
							disabled={creating}
							class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-4)] px-3 py-2 text-ui text-[var(--color-text-bright)] outline-none placeholder:text-[var(--color-text-muted)] transition-colors duration-150 focus:border-[var(--color-accent)] disabled:opacity-60"
						/>
					</div>
					<div>
						<label class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]" for="tmpl-memory">
							Memory MB
						</label>
						<input
							id="tmpl-memory"
							type="number"
							min="128"
							step="128"
							bind:value={createForm.memory_mb}
							disabled={creating}
							class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-4)] px-3 py-2 text-ui text-[var(--color-text-bright)] outline-none placeholder:text-[var(--color-text-muted)] transition-colors duration-150 focus:border-[var(--color-accent)] disabled:opacity-60"
						/>
					</div>
				</div>

				<div>
					<label class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]" for="tmpl-recipe">
						Recipe <span class="normal-case font-normal text-[var(--color-text-muted)]">(one instruction per line)</span>
					</label>
					<textarea
						id="tmpl-recipe"
						rows="7"
						placeholder={"RUN apt-get install -y python3 python3-pip\nWORKDIR /app\nENV PORT=8080\nRUN pip3 install numpy pandas\nSTART python3 server.py"}
						bind:value={createForm.recipe}
						disabled={creating}
						class="w-full resize-y rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-4)] px-3 py-2 font-mono text-meta leading-relaxed text-[var(--color-text-bright)] outline-none placeholder:text-[var(--color-text-muted)] transition-colors duration-150 focus:border-[var(--color-accent)] disabled:opacity-60"
					></textarea>
					<p class="mt-1 text-label text-[var(--color-text-muted)]">
						Supports <code class="font-mono">RUN</code>, <code class="font-mono">START</code>, <code class="font-mono">WORKDIR</code>, <code class="font-mono">ENV key=value</code>, <code class="font-mono">USER name</code>, <code class="font-mono">COPY src dst</code>. RUN steps have a 30s timeout; override with <code class="font-mono">RUN --timeout=5m</code>. COPY references files from the uploaded archive.
					</p>
				</div>

				<div>
					<label class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]" for="tmpl-archive">
						Build Archive <span class="normal-case font-normal text-[var(--color-text-muted)]">(optional, for COPY commands)</span>
					</label>
					<div class="flex items-center gap-3">
						<label
							class="flex cursor-pointer items-center gap-2 rounded-[var(--radius-button)] border border-[var(--color-border)] bg-[var(--color-bg-4)] px-3 py-2 text-ui text-[var(--color-text-secondary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)]"
						>
							<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="17 8 12 3 7 8"/><line x1="12" y1="3" x2="12" y2="15"/></svg>
							Choose file
							<input
								id="tmpl-archive"
								type="file"
								accept=".tar,.tar.gz,.tgz,.zip"
								disabled={creating}
								onchange={(e) => { const f = (e.target as HTMLInputElement).files?.[0]; createForm.archive = f ?? null; }}
								class="hidden"
							/>
						</label>
						{#if createForm.archive}
							<span class="flex items-center gap-1.5 text-meta text-[var(--color-text-secondary)]">
								<span class="font-mono">{createForm.archive.name}</span>
								<button
									onclick={() => { createForm.archive = null; }}
									class="text-[var(--color-text-muted)] hover:text-[var(--color-red)] transition-colors"
								>
									<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
								</button>
							</span>
						{:else}
							<span class="text-meta text-[var(--color-text-muted)]">tar, tar.gz, or zip</span>
						{/if}
					</div>
				</div>

				<div>
					<label class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]" for="tmpl-healthcheck">
						Healthcheck <span class="normal-case font-normal text-[var(--color-text-muted)]">(optional)</span>
					</label>
					<input
						id="tmpl-healthcheck"
						type="text"
						placeholder="e.g. curl -s http://localhost:8080/health"
						bind:value={createForm.healthcheck}
						disabled={creating}
						class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-4)] px-3 py-2 font-mono text-meta text-[var(--color-text-bright)] outline-none placeholder:text-[var(--color-text-muted)] transition-colors duration-150 focus:border-[var(--color-accent)] disabled:opacity-60"
					/>
					<p class="mt-1 text-label text-[var(--color-text-muted)]">
						If set, the build will poll this command every 1s (up to 60s) after the recipe completes. On success, a full snapshot (with memory state) is created. Without a healthcheck, only the rootfs is saved.
					</p>
				</div>

				<label class="flex cursor-pointer items-center gap-2.5">
					<input
						type="checkbox"
						bind:checked={createForm.skip_pre_post}
						disabled={creating}
						class="h-4 w-4 cursor-pointer rounded border border-[var(--color-border)] bg-[var(--color-bg-4)] accent-[var(--color-accent)]"
					/>
					<span class="text-ui text-[var(--color-text-secondary)]">Skip pre-build and post-build steps</span>
				</label>
			</div>

			<div class="mt-6 flex justify-end gap-3">
				<button
					onclick={() => (showCreate = false)}
					disabled={creating}
					class="rounded-[var(--radius-button)] border border-[var(--color-border)] px-4 py-2 text-ui text-[var(--color-text-secondary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)] disabled:opacity-50"
				>
					Cancel
				</button>
				<button
					onclick={handleCreate}
					disabled={creating || !createForm.name.trim() || !createForm.recipe.trim()}
					class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-accent)] px-5 py-2 text-ui font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0 disabled:opacity-50 disabled:hover:translate-y-0"
				>
					{#if creating}
						<svg class="animate-spin" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 12a9 9 0 1 1-6.219-8.56"/></svg>
						Creating…
					{:else}
						Start Build
					{/if}
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- ── Delete Template Confirmation ────────────────────────────────── -->
{#if deleteTarget}
	<div class="fixed inset-0 z-50 flex items-center justify-center">
		<div
			class="absolute inset-0 bg-black/60"
			role="button"
			tabindex="-1"
			onclick={() => { if (!deleting) deleteTarget = null; }}
			onkeydown={(e) => { if (e.key === 'Escape' && !deleting) deleteTarget = null; }}
		></div>
		<div
			class="relative w-full max-w-[420px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)] p-6 shadow-xl"
			style="animation: fadeUp 0.18s cubic-bezier(0.25,1,0.5,1) both"
		>
			<h2 class="font-serif text-[1.375rem] leading-tight tracking-[-0.02em] text-[var(--color-text-bright)]">
				Delete Template
			</h2>
			<p class="mt-1.5 text-ui text-[var(--color-text-tertiary)]">
				Permanently remove <code class="rounded bg-[var(--color-bg-4)] px-1.5 py-0.5 font-mono text-[0.8rem] text-[var(--color-text-primary)]">{deleteTarget.name}</code> from all hosts.
			</p>

			{#if deleteError}
				<div class="mt-3 rounded-[var(--radius-input)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-3 py-2 text-meta text-[var(--color-red)]">
					{deleteError}
				</div>
			{/if}

			<div class="mt-6 flex justify-end gap-3">
				<button
					onclick={() => (deleteTarget = null)}
					disabled={deleting}
					class="rounded-[var(--radius-button)] border border-[var(--color-border)] px-4 py-2 text-ui text-[var(--color-text-secondary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)] disabled:opacity-50"
				>
					Cancel
				</button>
				<button
					onclick={handleDeleteTemplate}
					disabled={deleting}
					class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-red)] px-5 py-2 text-ui font-semibold text-white transition-all duration-150 hover:brightness-110 disabled:opacity-50"
				>
					{#if deleting}
						<svg class="animate-spin" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 12a9 9 0 1 1-6.219-8.56"/></svg>
						Deleting…
					{:else}
						Delete
					{/if}
				</button>
			</div>
		</div>
	</div>
{/if}

<style>
	@keyframes fadeUp {
		from { opacity: 0; transform: translateY(10px); }
		to { opacity: 1; transform: translateY(0); }
	}

	@keyframes shimmer {
		0% { background-position: -200% 0; }
		100% { background-position: 200% 0; }
	}

	.skeleton {
		background: linear-gradient(
			90deg,
			var(--color-bg-3) 25%,
			var(--color-bg-4) 50%,
			var(--color-bg-3) 75%
		);
		background-size: 200% 100%;
		animation: shimmer 1.4s ease infinite;
	}
</style>
