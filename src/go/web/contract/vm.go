package contract

import "phenix/internal/mm"

type VMList struct {
	VMs   []mm.VM `json:"vms"`
	Total int     `json:"total"`
}

func NewVMList(vms []mm.VM) VMList {
	return VMList{
		VMs:   vms,
		Total: len(vms),
	}
}

type VMInterface struct {
	Index int    `json:"index"`
	VLAN  string `json:"vlan"`
}

type UpdateVMRequest struct {
	Name       string       `json:"name"`
	Experiment string       `json:"exp"`
	CPUs       int          `json:"cpus"`
	RAM        int          `json:"ram"`
	Disk       string       `json:"disk"`
	DoNotBoot  *bool        `json:"dnb"`
	Interface  *VMInterface `json:"interface"`
	Host       *string      `json:"host"`
}

type VMRedeployRequest struct {
	Name    string `json:"name"`
	CPUs    int    `json:"cpus"`
	RAM     int    `json:"ram"`
	Disk    string `json:"disk"`
	Injects bool   `json:"injects"`
}

type StartCaptureRequest struct {
	Interface int    `json:"interface"`
	Filename  string `json:"filename"`
}

type SnapshotRequest struct {
	Filename string `json:"filename"`
}

type BackingImageRequest struct {
	Filename string `json:"filename"`
}

type BackingImageResponse struct {
	Disk string `json:"disk"`
	VM   *mm.VM `json:"vm,omitempty"`
}

func NewBackingImageResponse(disk string, vm *mm.VM) BackingImageResponse {
	return BackingImageResponse{
		Disk: disk,
		VM:   vm,
	}
}
