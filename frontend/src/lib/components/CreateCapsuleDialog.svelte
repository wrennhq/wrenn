<script lang="ts">
	import { createCapsule, listSnapshots, type Capsule, type CreateCapsuleParams, type Snapshot } from '$lib/api/capsules';

	type Props = {
		open: boolean;
		onclose: () => void;
		oncreated?: (capsule: Capsule) => void;
	};
	let { open, onclose, oncreated }: Props = $props();

	let createForm = $state<CreateCapsuleParams>({ template: 'minimal', vcpus: 1, memory_mb: 512, timeout_sec: 0 });
	let creating = $state(false);
	let createError = $state<string | null>(null);

	// Template combobox state
	let templates = $state<Snapshot[]>([]);
	let templatesLoading = $state(false);
	let templateQuery = $state('');
	let comboOpen = $state(false);
	let highlightIdx = $state(-1);
	let inputEl = $state<HTMLInputElement | undefined>(undefined);
	let listEl = $state<HTMLUListElement | undefined>(undefined);

	// Resolve selected template for type indicator + snapshot locking
	let selectedTemplate = $derived(
		templates.find((t) => t.name === createForm.template)
	);
	let selectedIsSnapshot = $derived(selectedTemplate?.type === 'snapshot');

	let filtered = $derived.by(() => {
		const q = templateQuery.toLowerCase();
		if (!q) return templates;
		return templates.filter((t) => t.name.toLowerCase().includes(q));
	});

	// Fetch templates when dialog opens
	$effect(() => {
		if (open && templates.length === 0 && !templatesLoading) {
			templatesLoading = true;
			listSnapshots().then((result) => {
				if (result.ok) templates = result.data;
				templatesLoading = false;
			});
		}
		if (open) {
			templateQuery = createForm.template ?? '';
		}
	});

	function selectTemplate(t: Snapshot) {
		createForm.template = t.name;
		templateQuery = t.name;
		// Pre-fill specs from the template if available
		if (t.vcpus) createForm.vcpus = t.vcpus;
		if (t.memory_mb) createForm.memory_mb = t.memory_mb;
		comboOpen = false;
		highlightIdx = -1;
	}

	function handleInputKeydown(e: KeyboardEvent) {
		if (!comboOpen && (e.key === 'ArrowDown' || e.key === 'ArrowUp')) {
			comboOpen = true;
			highlightIdx = 0;
			e.preventDefault();
			return;
		}
		if (!comboOpen) return;

		if (e.key === 'ArrowDown') {
			e.preventDefault();
			highlightIdx = Math.min(highlightIdx + 1, filtered.length - 1);
			scrollToHighlighted();
		} else if (e.key === 'ArrowUp') {
			e.preventDefault();
			highlightIdx = Math.max(highlightIdx - 1, 0);
			scrollToHighlighted();
		} else if (e.key === 'Enter' && highlightIdx >= 0 && highlightIdx < filtered.length) {
			e.preventDefault();
			selectTemplate(filtered[highlightIdx]);
		} else if (e.key === 'Escape') {
			comboOpen = false;
			highlightIdx = -1;
		}
	}

	function scrollToHighlighted() {
		if (!listEl) return;
		const item = listEl.children[highlightIdx] as HTMLElement | undefined;
		item?.scrollIntoView({ block: 'nearest' });
	}

	function handleInputFocus() {
		comboOpen = true;
		highlightIdx = -1;
	}

	function handleInputBlur() {
		// Delay to allow click on dropdown item to fire first
		setTimeout(() => {
			comboOpen = false;
			// If the typed query matches an existing template, apply it
			const match = templates.find((t) => t.name === templateQuery);
			if (match) {
				createForm.template = match.name;
			} else {
				// Allow free-form entry (user might know a template name not in the list)
				createForm.template = templateQuery;
			}
		}, 150);
	}

	async function handleCreate() {
		creating = true;
		createError = null;
		const result = await createCapsule(createForm);
		if (result.ok) {
			createForm = { template: 'minimal', vcpus: 1, memory_mb: 512, timeout_sec: 0 };
			templateQuery = 'minimal';
			oncreated?.(result.data);
			onclose();
		} else {
			createError = result.error;
		}
		creating = false;
	}
</script>

{#if open}
	<div class="fixed inset-0 z-50 flex items-center justify-center">
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="absolute inset-0 bg-black/60"
			onclick={() => { if (!creating) onclose(); }}
			onkeydown={(e) => { if (e.key === 'Escape' && !creating) onclose(); }}
		></div>

		<div class="relative w-full max-w-[420px] rounded-[var(--radius-card)] border border-[var(--color-border-mid)] bg-[var(--color-bg-2)]" style="animation: fadeUp 0.2s ease both; box-shadow: var(--shadow-dialog)">
			<div class="p-6">
			<h2 class="font-serif text-heading tracking-[-0.02em] text-[var(--color-text-bright)]">Launch Capsule</h2>
			<p class="mt-1 text-ui text-[var(--color-text-tertiary)]">Configure resources and launch. The VM will be ready in under a second.</p>

			{#if createError}
				<div class="mt-4 rounded-[var(--radius-input)] border border-[var(--color-red)]/30 bg-[var(--color-red)]/5 px-3 py-2 text-meta text-[var(--color-red)]">
					{createError}
				</div>
			{/if}

			<div class="mt-5 space-y-4">
				<!-- Template combobox -->
				<div class="relative">
					<label class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]" for="create-template">Template</label>
					<div class="relative">
						{#if selectedTemplate}
							<span class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 h-1.5 w-1.5 rounded-full {selectedTemplate.type === 'snapshot' ? 'bg-[var(--color-accent)]' : 'bg-[var(--color-blue)]'}"></span>
						{/if}
						<input
							bind:this={inputEl}
							id="create-template"
							type="text"
							role="combobox"
							aria-expanded={comboOpen}
							aria-autocomplete="list"
							aria-controls="template-listbox"
							autocomplete="off"
							bind:value={templateQuery}
							onfocus={handleInputFocus}
							onblur={handleInputBlur}
							onkeydown={handleInputKeydown}
							disabled={creating}
							class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-4)] py-2 pr-8 font-mono text-ui text-[var(--color-text-bright)] outline-none placeholder:text-[var(--color-text-muted)] transition-colors duration-150 focus:border-[var(--color-accent)] disabled:opacity-60 {selectedTemplate ? 'pl-7' : 'pl-3'}"
							placeholder="Search templates..."
						/>
						<!-- Chevron -->
						<svg
							class="pointer-events-none absolute right-2.5 top-1/2 -translate-y-1/2 text-[var(--color-text-muted)] transition-transform duration-150 {comboOpen ? 'rotate-180' : ''}"
							width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"
						>
							<polyline points="6 9 12 15 18 9" />
						</svg>
					</div>

					<!-- Dropdown -->
					{#if comboOpen}
						<ul
							bind:this={listEl}
							id="template-listbox"
							role="listbox"
							class="absolute z-10 mt-1 max-h-[200px] w-full overflow-y-auto rounded-[var(--radius-input)] border border-[var(--color-border-mid)] bg-[var(--color-bg-3)] py-1 shadow-lg"
							style="animation: fadeUp 0.12s ease both"
						>
							{#if templatesLoading}
								<li class="flex items-center gap-2 px-3 py-2.5 text-meta text-[var(--color-text-muted)]">
									<svg class="animate-spin" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 12a9 9 0 1 1-6.219-8.56" /></svg>
									Loading templates...
								</li>
							{:else if filtered.length === 0}
								<li class="px-3 py-2.5 text-meta text-[var(--color-text-muted)]">
									{templateQuery ? 'No matching templates' : 'No templates available'}
								</li>
							{:else}
								{#each filtered as t, i (t.name)}
									<!-- svelte-ignore a11y_click_events_have_key_events -->
									<li
										role="option"
										aria-selected={i === highlightIdx}
										class="flex cursor-pointer items-center gap-2.5 px-3 py-2 transition-colors duration-75
											{i === highlightIdx
												? 'bg-[var(--color-bg-5)] text-[var(--color-text-bright)]'
												: 'text-[var(--color-text-primary)] hover:bg-[var(--color-bg-4)]'}
											{createForm.template === t.name ? 'font-medium' : ''}"
										onmousedown={(e) => { e.preventDefault(); selectTemplate(t); }}
										onmouseenter={() => { highlightIdx = i; }}
									>
										<!-- Type badge -->
										{#if t.type === 'snapshot'}
											<span class="inline-flex shrink-0 items-center rounded-full border border-[var(--color-accent)]/25 bg-[var(--color-accent)]/8 px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-[0.04em] text-[var(--color-accent-bright)]">
												snap
											</span>
										{:else}
											<span class="inline-flex shrink-0 items-center rounded-full border border-[var(--color-blue)]/25 bg-[var(--color-blue)]/8 px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-[0.04em] text-[var(--color-blue)]">
												base
											</span>
										{/if}
										<span class="truncate font-mono text-meta">{t.name}</span>
										<!-- Specs hint -->
										{#if t.vcpus && t.memory_mb}
											<span class="ml-auto shrink-0 text-[10px] text-[var(--color-text-muted)]">
												{t.vcpus}v · {t.memory_mb}MB
											</span>
										{/if}
										<!-- Selected check -->
										{#if createForm.template === t.name}
											<svg class="ml-auto shrink-0 text-[var(--color-accent)]" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
												<polyline points="20 6 9 17 4 12" />
											</svg>
										{/if}
									</li>
								{/each}
							{/if}
						</ul>
					{/if}

					<p class="mt-1.5 text-meta text-[var(--color-text-muted)]">Snapshot or base image to boot from.</p>
				</div>

				<div class="grid grid-cols-2 gap-3">
					<div>
						<label class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]" for="create-vcpus">vCPUs</label>
						<input
							id="create-vcpus"
							type="number"
							min="1"
							max="8"
							bind:value={createForm.vcpus}
							disabled={creating || selectedIsSnapshot}
							class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-4)] px-3 py-2 font-mono text-ui text-[var(--color-text-bright)] outline-none transition-colors duration-150 focus:border-[var(--color-accent)] disabled:opacity-60"
						/>
					</div>
					<div>
						<label class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]" for="create-memory">Memory (MB)</label>
						<input
							id="create-memory"
							type="number"
							min="128"
							max="8192"
							step="128"
							bind:value={createForm.memory_mb}
							disabled={creating || selectedIsSnapshot}
							class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-4)] px-3 py-2 font-mono text-ui text-[var(--color-text-bright)] outline-none transition-colors duration-150 focus:border-[var(--color-accent)] disabled:opacity-60"
						/>
					</div>
				</div>

				<div>
					<label class="mb-1.5 block text-label font-semibold uppercase tracking-[0.05em] text-[var(--color-text-tertiary)]" for="create-timeout">Idle timeout</label>
					<input
						id="create-timeout"
						type="number"
						min="0"
						bind:value={createForm.timeout_sec}
						disabled={creating}
						class="w-full rounded-[var(--radius-input)] border border-[var(--color-border)] bg-[var(--color-bg-4)] px-3 py-2 font-mono text-ui text-[var(--color-text-bright)] outline-none transition-colors duration-150 focus:border-[var(--color-accent)] disabled:opacity-60"
						placeholder="0"
					/>
					<p class="mt-1.5 text-meta text-[var(--color-text-muted)]">Seconds of inactivity before the capsule pauses. Set to 0 to keep it running indefinitely.</p>
				</div>
			</div>

			<div class="mt-6 flex justify-end gap-3">
				<button
					onclick={onclose}
					disabled={creating}
					class="rounded-[var(--radius-button)] border border-[var(--color-border)] px-4 py-2 text-ui text-[var(--color-text-secondary)] transition-colors duration-150 hover:border-[var(--color-border-mid)] hover:text-[var(--color-text-primary)] disabled:opacity-50"
				>
					Cancel
				</button>
				<button
					onclick={handleCreate}
					disabled={creating || !templateQuery.trim()}
					class="flex items-center gap-2 rounded-[var(--radius-button)] bg-[var(--color-accent)] px-5 py-2 text-ui font-semibold text-white transition-all duration-150 hover:brightness-115 hover:-translate-y-px active:translate-y-0 disabled:opacity-50 disabled:hover:translate-y-0"
				>
					{#if creating}
						<svg class="animate-spin" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
							<path d="M21 12a9 9 0 1 1-6.219-8.56" />
						</svg>
						Launching...
					{:else}
						Launch
					{/if}
				</button>
			</div>
			</div>
		</div>
	</div>
{/if}
