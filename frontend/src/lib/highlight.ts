/**
 * Lazy syntax highlighting via shiki.
 *
 * The highlighter WASM engine + theme are loaded on first use.
 * Language grammars load on-demand per extension.
 * All imports are dynamic so nothing touches the main bundle.
 */

import type { HighlighterGeneric, ThemedToken } from 'shiki';
export type { ThemedToken };

// eslint-disable-next-line @typescript-eslint/no-explicit-any
let highlighter: HighlighterGeneric<any, any> | null = null;
// eslint-disable-next-line @typescript-eslint/no-explicit-any
let loadingPromise: Promise<HighlighterGeneric<any, any>> | null = null;

const THEME = 'vesper';

// Extensions → shiki language IDs.
// Only map what we expect users to encounter in sandboxes.
const EXT_TO_LANG: Record<string, string> = {
	// Go
	go: 'go', mod: 'go', sum: 'go',
	// Python
	py: 'python', pyi: 'python', pyx: 'python',
	// JavaScript / TypeScript
	js: 'javascript', mjs: 'javascript', cjs: 'javascript', jsx: 'jsx',
	ts: 'typescript', mts: 'typescript', cts: 'typescript', tsx: 'tsx',
	// Rust
	rs: 'rust',
	// Shell
	sh: 'shellscript', bash: 'shellscript', zsh: 'shellscript',
	// Config
	json: 'json', yaml: 'yaml', yml: 'yaml', toml: 'toml', ini: 'ini',
	env: 'shellscript',
	// Markup / docs
	md: 'markdown', mdx: 'mdx', html: 'html', htm: 'html', xml: 'xml',
	// CSS
	css: 'css', scss: 'scss', less: 'less',
	// SQL
	sql: 'sql',
	// Svelte / Vue
	svelte: 'svelte', vue: 'vue',
	// Docker / Make
	dockerfile: 'dockerfile',
	makefile: 'makefile',
	// Proto
	proto: 'protobuf',
	// C / C++
	c: 'c', h: 'c', cpp: 'cpp', cc: 'cpp', cxx: 'cpp', hpp: 'cpp',
	// Java / Kotlin
	java: 'java', kt: 'kotlin', kts: 'kotlin',
	// Ruby
	rb: 'ruby',
	// PHP
	php: 'php',
	// Lua
	lua: 'lua',
	// Misc
	txt: 'plaintext',
};

// Filenames without extensions
const NAME_TO_LANG: Record<string, string> = {
	Dockerfile: 'dockerfile',
	Makefile: 'makefile',
	Containerfile: 'dockerfile',
	Vagrantfile: 'ruby',
};

/** Resolve a filename to a shiki language ID, or null if unknown. */
export function langFromFilename(name: string): string | null {
	// Check full filename first (Dockerfile, Makefile, etc.)
	const basename = name.includes('/') ? name.slice(name.lastIndexOf('/') + 1) : name;
	if (NAME_TO_LANG[basename]) return NAME_TO_LANG[basename];

	const dot = basename.lastIndexOf('.');
	if (dot <= 0) return null;
	const ext = basename.slice(dot + 1).toLowerCase();
	return EXT_TO_LANG[ext] ?? null;
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
async function getHighlighter(): Promise<HighlighterGeneric<any, any>> {
	if (highlighter) return highlighter;
	if (loadingPromise) return loadingPromise;

	loadingPromise = (async () => {
		const { createHighlighter } = await import('shiki');

		const h = await createHighlighter({
			themes: [THEME],
			langs: [], // load languages on demand
		});
		highlighter = h;
		return h;
	})();

	return loadingPromise;
}

/**
 * Tokenize code for a given language.
 * Returns an array of lines, each containing themed tokens with `color` and `content`.
 * Returns null if the language is unknown or highlighting fails.
 */
export async function tokenize(
	code: string,
	filename: string,
): Promise<ThemedToken[][] | null> {
	const lang = langFromFilename(filename);
	if (!lang || lang === 'plaintext') return null;

	try {
		const h = await getHighlighter();

		// Load grammar on demand if not yet loaded
		const loaded = h.getLoadedLanguages();
		if (!loaded.includes(lang)) {
			await h.loadLanguage(lang);
		}

		return h.codeToTokensBase(code, { lang, theme: THEME });
	} catch {
		// Grammar not available or other error — fall back to plain text
		return null;
	}
}
