package models

// FleetMachine is a physical host in the fleet topology.
// Hard-coded so the dashboard tree and the /api/proxy machine_id validation
// share a single source of truth. When a new physical PC is added, edit Fleet
// here.
type FleetMachine struct {
	ID       string      `json:"id"`
	Name     string      `json:"name"`
	Hostname string      `json:"hostname"`
	Kind     string      `json:"kind"` // "main" | "mini"
	VMs      []FleetVM   `json:"vms,omitempty"`
}

type FleetVM struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Fleet is the canonical machine list. Any machine_id used by a scraper must
// match either a FleetMachine.ID (running on the host directly) or a
// FleetVM.ID (running inside a VM).
var Fleet = []FleetMachine{
	{
		ID:       "main_machine",
		Name:     "Main Machine",
		Hostname: "main.local",
		Kind:     "main",
		VMs: []FleetVM{
			{ID: "main_machine_vm1", Name: "VM1"},
			{ID: "main_machine_vm2", Name: "VM2"},
			{ID: "main_machine_vm3", Name: "VM3"},
			{ID: "main_machine_vm4", Name: "VM4"},
			{ID: "main_machine_vm5", Name: "VM5"},
			{ID: "main_machine_vm11", Name: "VM11"},
			{ID: "main_machine_vm13", Name: "VM13"},
			{ID: "main_machine_vm15", Name: "VM15"},
			{ID: "main_machine_vm16", Name: "VM16"},
			{ID: "main_machine_vm17", Name: "VM17"},
			{ID: "main_machine_vm18", Name: "VM18"},
			{ID: "main_machine_vm19", Name: "VM19"},
		},
	},
	{
		ID:       "mini_pc_ui",
		Name:     "Mini PC UI",
		Hostname: "mini-ui.local",
		Kind:     "mini",
	},
	{
		ID:       "mini_pc_03",
		Name:     "Mini PC 03",
		Hostname: "mini03.local",
		Kind:     "mini",
		VMs: []FleetVM{
			{ID: "mini_pc_03_vm1", Name: "VM1"},
			{ID: "mini_pc_03_vm2", Name: "VM2"},
			{ID: "mini_pc_03_vm3", Name: "VM3"},
			{ID: "mini_pc_03_vm4", Name: "VM4"},
			{ID: "mini_pc_03_vm5", Name: "VM5"},
		},
	},
	{
		ID:       "mini_pc_04",
		Name:     "Mini PC 04",
		Hostname: "mini04.local",
		Kind:     "mini",
	},
}

// IsValidMachineID reports whether the given machine_id matches a known host
// or VM in the Fleet topology.
func IsValidMachineID(id string) bool {
	for _, m := range Fleet {
		if m.ID == id {
			return true
		}
		for _, v := range m.VMs {
			if v.ID == id {
				return true
			}
		}
	}
	return false
}

// FleetMachineIDs returns every host + VM id, in declaration order.
func FleetMachineIDs() []string {
	ids := make([]string, 0, len(Fleet)*2)
	for _, m := range Fleet {
		ids = append(ids, m.ID)
		for _, v := range m.VMs {
			ids = append(ids, v.ID)
		}
	}
	return ids
}
