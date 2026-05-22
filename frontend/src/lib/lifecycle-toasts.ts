import type { SSEEvent } from '$lib/api/events';
import { toast } from '$lib/toast.svelte';

// Terminal copy per lifecycle verb. Success and failure are paired so the two
// can never drift apart.
const VERBS: Record<string, { done: string; failed: string }> = {
	'capsule.create': { done: 'Capsule created', failed: 'Capsule failed to start' },
	'capsule.pause': { done: 'Capsule paused', failed: 'Capsule failed to pause' },
	'capsule.resume': { done: 'Capsule resumed', failed: 'Capsule failed to resume' },
	'capsule.destroy': { done: 'Capsule destroyed', failed: 'Capsule failed to destroy' }
};

/**
 * Surfaces lifecycle outcomes as toasts. Only system-actor events with an
 * outcome are terminal: the user-actor events published at request-accept time
 * carry a premature outcome (the operation has only been accepted, not yet
 * completed) and are skipped, so each operation toasts exactly once.
 */
export function lifecycleToast(event: SSEEvent): void {
	if (event.actor?.type !== 'system' || !event.outcome) return;

	if (event.event === 'template.snapshot.create') {
		const name = event.resource?.id;
		if (event.outcome === 'success') {
			toast.success(name ? `Snapshot "${name}" captured` : 'Snapshot captured');
		} else {
			toast.error(event.error ? `Snapshot failed: ${event.error}` : 'Snapshot failed');
		}
		return;
	}

	const verb = VERBS[event.event];
	if (!verb) return;
	if (event.outcome === 'success') {
		toast.success(verb.done);
	} else {
		toast.error(event.error ? `${verb.failed}: ${event.error}` : verb.failed);
	}
}
