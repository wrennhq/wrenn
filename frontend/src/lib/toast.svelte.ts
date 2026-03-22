type Toast = { id: string; message: string; type: 'error' | 'success' };

let toasts = $state<Toast[]>([]);

export const toast = {
	get list() {
		return toasts;
	},
	error(message: string, duration = 4000) {
		const id = Math.random().toString(36).slice(2);
		toasts = [...toasts, { id, message, type: 'error' }];
		setTimeout(() => this.dismiss(id), duration);
	},
	success(message: string, duration = 3000) {
		const id = Math.random().toString(36).slice(2);
		toasts = [...toasts, { id, message, type: 'success' }];
		setTimeout(() => this.dismiss(id), duration);
	},
	dismiss(id: string) {
		toasts = toasts.filter((t) => t.id !== id);
	}
};
