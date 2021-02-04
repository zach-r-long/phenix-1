package contract

import (
	"phenix/internal/mm"
	"phenix/types"
	"phenix/web/cache"
)

type VLAN struct {
	VLAN  int    `json:"vlan"`
	Alias string `json:"alias"`
}

type Experiment struct {
	Name      string   `json:"name"`
	Topology  string   `json:"topology"`
	Scenario  string   `json:"scenario,omitempty"`
	StartTime string   `json:"start_time,omitempty"`
	Running   bool     `json:"running"`
	Status    string   `json:"status"`
	VLANMin   int      `json:"vlan_min"`
	VLANMax   int      `json:"vlan_max"`
	VLANs     []VLAN   `json:"vlans"`
	VMs       []mm.VM  `json:"vms"`
	Apps      []string `json:"apps,omitempty"`

	// TODO: depricate
	VLANCount int `json:"vlan_count"`
	VMCount   int `json:"vm_count"`
}

func NewExperiment(exp types.Experiment, status cache.Status, vms []mm.VM) Experiment {
	e := Experiment{
		Name:      exp.Spec.ExperimentName(),
		Topology:  exp.Metadata.Annotations["topology"],
		Scenario:  exp.Metadata.Annotations["scenario"],
		StartTime: exp.Status.StartTime(),
		Running:   exp.Running(),
		Status:    string(status),
		VMs:       vms,
		VMCount:   len(vms),
	}

	var apps []string

	for _, app := range exp.Apps() {
		apps = append(apps, app.Name())
	}

	var aliases map[string]int

	if exp.Running() {
		aliases = exp.Status.VLANs()

		var (
			min = 0
			max = 0
		)

		for _, k := range exp.Status.VLANs() {
			if min == 0 || k < min {
				min = k
			}

			if max == 0 || k > max {
				max = k
			}
		}

		e.VLANMin = min
		e.VLANMax = max
	} else {
		aliases = exp.Spec.VLANs().Aliases()

		e.VLANMin = exp.Spec.VLANs().Min()
		e.VLANMax = exp.Spec.VLANs().Max()
	}

	if aliases != nil {
		var vlans []VLAN

		for alias := range aliases {
			vlan := VLAN{
				VLAN:  aliases[alias],
				Alias: alias,
			}

			vlans = append(vlans, vlan)
		}

		e.VLANs = vlans
		e.VLANCount = len(aliases)
	}

	return e
}

type CreateExperimentRequest struct {
	Name     string `json:"name"`
	Topology string `json:"topology"`
	Scenario string `json:"scenario"`
	VLANMin  int    `json:"vlan_min"`
	VLANMax  int    `json:"vlan_max"`
}

type Schedule struct {
	VM           string `json:"vm"`
	Host         string `json:"host"`
	AutoAssigned bool   `json:"auto_assigned"`
}

type ExperimentSchedule struct {
	Schedule []Schedule `json:"schedule"`
}

func NewExperimentSchedule(exp types.Experiment) ExperimentSchedule {
	var sched []Schedule

	for vm, host := range exp.Spec.Schedules() {
		sched = append(sched, Schedule{VM: vm, Host: host})
	}

	return ExperimentSchedule{Schedule: sched}
}

type UpdateExperimentScheduleRequest struct {
	Algorithm string `json:"algorithm"`
}
