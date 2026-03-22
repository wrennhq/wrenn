export function getInitialCollapsed(): boolean {
	return typeof window !== 'undefined'
		? localStorage.getItem('wrenn_sidebar_collapsed') === 'true'
		: false;
}
