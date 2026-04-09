import { listTeams, type TeamWithRole } from '$lib/api/team';

function createTeamsStore() {
	let teams = $state<TeamWithRole[]>([]);
	let loaded = $state(false);

	return {
		get list() {
			return teams;
		},
		get loaded() {
			return loaded;
		},
		async fetch() {
			if (loaded) return;
			const result = await listTeams();
			if (result.ok) {
				teams = result.data;
				loaded = true;
			}
		},
		// Call after mutating teams (create/switch triggers a full reload, but
		// adding a team locally avoids a flicker in the popover list).
		set(newTeams: TeamWithRole[]) {
			teams = newTeams;
			loaded = true;
		},
		reset() {
			teams = [];
			loaded = false;
		}
	};
}

export const teams = createTeamsStore();
