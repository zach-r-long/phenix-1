package web

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"sort"
	"strconv"
	"time"

	"phenix/api/cluster"
	"phenix/api/config"
	"phenix/api/experiment"
	"phenix/api/scenario"
	"phenix/api/vm"
	"phenix/internal/mm"
	"phenix/types"
	v1 "phenix/types/version/v1"
	putil "phenix/util"
	"phenix/web/broker"
	"phenix/web/cache"
	"phenix/web/contract"
	"phenix/web/rbac"
	"phenix/web/util"

	log "github.com/activeshadow/libminimega/minilog"
	"github.com/dgrijalva/jwt-go"
	assetfs "github.com/elazarl/go-bindata-assetfs"
	"github.com/gorilla/mux"
	"github.com/pkg/errors"
	"golang.org/x/net/websocket"
	"golang.org/x/sync/errgroup"
)

// GET /experiments
func GetExperiments(w http.ResponseWriter, r *http.Request) {
	log.Debug("GetExperiments HTTP handler called")

	var (
		ctx   = r.Context()
		role  = ctx.Value("role").(rbac.Role)
		query = r.URL.Query()
		size  = query.Get("screenshot")
	)

	if !role.Allowed("experiments", "", "list") {
		log.Warn("listing experiments not allowed for %s", ctx.Value("user").(string))
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	experiments, err := experiment.List()
	if err != nil {
		log.Error("getting experiments - %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	allowed := []contract.Experiment{}

	for _, exp := range experiments {
		if !role.Allowed("experiments", exp.Metadata.Name, "list") {
			continue
		}

		// This will happen if another handler is currently acting on the
		// experiment.
		status := isExperimentLocked(exp.Metadata.Name)

		if status == "" {
			if exp.Status.Running() {
				status = cache.StatusStarted
			} else {
				status = cache.StatusStopped
			}
		}

		// TODO: limit per-experiment VMs based on RBAC

		vms, err := vm.List(exp.Spec.ExperimentName)
		if err != nil {
			// TODO
		}

		if exp.Status.Running() && size != "" {
			for i, v := range vms {
				if !v.Running {
					continue
				}

				screenshot, err := util.GetScreenshot(exp.Spec.ExperimentName, v.Name, size)
				if err != nil {
					log.Error("getting screenshot - %v", err)
					continue
				}

				v.Screenshot = "data:image/png;base64," + base64.StdEncoding.EncodeToString(screenshot)

				vms[i] = v
			}
		}

		allowed = append(allowed, contract.NewExperiment(exp, status, vms))
	}

	body, err := json.Marshal(util.WithRoot("experiments", allowed))
	if err != nil {
		log.Error("marshaling experiments - %v", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	w.Write(body)
}

// POST /experiments
func CreateExperiment(w http.ResponseWriter, r *http.Request) {
	log.Debug("CreateExperiment HTTP handler called")

	var (
		ctx  = r.Context()
		role = ctx.Value("role").(rbac.Role)
	)

	if !role.Allowed("experiments", "", "create") {
		log.Warn("creating experiments not allowed for %s", ctx.Value("user").(string))
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		log.Error("reading request body - %v", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	var req contract.CreateExperimentRequest
	if err := json.Unmarshal(body, &req); err != nil {
		log.Error("unmashaling request body - %v", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	if err := lockExperimentForCreation(req.Name); err != nil {
		log.Warn(err.Error())
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	defer unlockExperiment(req.Name)

	opts := []experiment.CreateOption{
		experiment.CreateWithName(req.Name),
		experiment.CreateWithTopology(req.Topology),
		experiment.CreateWithScenario(req.Scenario),
		experiment.CreateWithVLANMin(req.VLANMin),
		experiment.CreateWithVLANMax(req.VLANMax),
	}

	if err := experiment.Create(ctx, opts...); err != nil {
		log.Error("creating experiment %s - %v", req.Name, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if warns := putil.Warnings(ctx); warns != nil {
		for _, warn := range warns {
			log.Warn("%v", warn)
		}
	}

	exp, err := experiment.Get(req.Name)
	if err != nil {
		log.Error("getting experiment %s - %v", req.Name, err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}

	vms, err := vm.List(req.Name)
	if err != nil {
		// TODO
	}

	body, err = json.Marshal(contract.NewExperiment(*exp, "", vms))
	if err != nil {
		log.Error("marshaling experiment %s - %v", req.Name, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	broker.Broadcast(
		broker.NewRequestPolicy("experiments", req.Name, "get"),
		broker.NewResource("experiment", req.Name, "create"),
		body,
	)
}

// GET /experiments/{name}
func GetExperiment(w http.ResponseWriter, r *http.Request) {
	log.Debug("GetExperiment HTTP handler called")

	var (
		ctx     = r.Context()
		role    = ctx.Value("role").(rbac.Role)
		vars    = mux.Vars(r)
		name    = vars["name"]
		query   = r.URL.Query()
		size    = query.Get("screenshot")
		sortCol = query.Get("sortCol")
		sortDir = query.Get("sortDir")
		pageNum = query.Get("pageNum")
		perPage = query.Get("perPage")
		showDNB = query.Get("show_dnb") != ""
	)

	if !role.Allowed("experiments", name, "get") {
		log.Warn("getting experiment %s not allowed for %s", name, ctx.Value("user").(string))
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	exp, err := experiment.Get(name)
	if err != nil {
		log.Error("getting experiment %s - %v", name, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	vms, err := vm.List(name)
	if err != nil {
		// TODO
	}

	// This will happen if another handler is currently acting on the
	// experiment.
	status := isExperimentLocked(name)
	allowed := mm.VMs{}

	for _, vm := range vms {
		if vm.DoNotBoot && !showDNB {
			continue
		}

		if role.Allowed("vms", name, "list", vm.Name) {
			if vm.Running && size != "" {
				screenshot, err := util.GetScreenshot(name, vm.Name, size)
				if err != nil {
					log.Error("getting screenshot: %v", err)
				} else {
					vm.Screenshot = "data:image/png;base64," + base64.StdEncoding.EncodeToString(screenshot)
				}
			}

			allowed = append(allowed, vm)
		}
	}

	if sortCol != "" && sortDir != "" {
		allowed.SortBy(sortCol, sortDir == "asc")
	}

	if pageNum != "" && perPage != "" {
		n, _ := strconv.Atoi(pageNum)
		s, _ := strconv.Atoi(perPage)

		allowed = allowed.Paginate(n, s)
	}

	body, err := json.Marshal(contract.NewExperiment(*exp, status, allowed))
	if err != nil {
		log.Error("marshaling experiment %s - %v", name, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Write(body)
}

// DELETE /experiments/{name}
func DeleteExperiment(w http.ResponseWriter, r *http.Request) {
	log.Debug("DeleteExperiment HTTP handler called")

	var (
		ctx  = r.Context()
		role = ctx.Value("role").(rbac.Role)
		vars = mux.Vars(r)
		name = vars["name"]
	)

	if !role.Allowed("experiments", name, "delete") {
		log.Warn("deleting experiment %s not allowed for %s", name, ctx.Value("user").(string))
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if err := lockExperimentForDeletion(name); err != nil {
		log.Warn(err.Error())
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	defer unlockExperiment(name)

	if err := experiment.Delete(name); err != nil {
		log.Error("deleting experiment %s - %v", name, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	broker.Broadcast(
		broker.NewRequestPolicy("experiments", name, "get"),
		broker.NewResource("experiment", name, "delete"),
		nil,
	)

	w.WriteHeader(http.StatusNoContent)
}

// POST /experiments/{name}/start
func StartExperiment(w http.ResponseWriter, r *http.Request) {
	log.Debug("StartExperiment HTTP handler called")

	var (
		ctx  = r.Context()
		role = ctx.Value("role").(rbac.Role)
		vars = mux.Vars(r)
		name = vars["name"]
	)

	if !role.Allowed("experiments/start", name, "update") {
		log.Warn("starting experiment %s not allowed for %s", name, ctx.Value("user").(string))
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if err := lockExperimentForStarting(name); err != nil {
		log.Warn(err.Error())
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	defer unlockExperiment(name)

	broker.Broadcast(
		broker.NewRequestPolicy("experiments", name, "get"),
		broker.NewResource("experiment", name, "starting"),
		nil,
	)

	type result struct {
		exp *types.Experiment
		err error
	}

	status := make(chan result)

	go func() {
		if err := experiment.Start(experiment.StartWithName(name)); err != nil {
			status <- result{nil, err}
		}

		exp, err := experiment.Get(name)
		status <- result{exp, err}
	}()

	var progress float64
	count, _ := vm.Count(name)

	for {
		select {
		case s := <-status:
			if s.err != nil {
				broker.Broadcast(
					broker.NewRequestPolicy("experiments", name, "get"),
					broker.NewResource("experiment", name, "errorStarting"),
					nil,
				)

				log.Error("starting experiment %s - %v", name, s.err)
				http.Error(w, s.err.Error(), http.StatusBadRequest)
				return
			}

			vms, err := vm.List(name)
			if err != nil {
				// TODO
			}

			body, err := json.Marshal(contract.NewExperiment(*s.exp, "", vms))
			if err != nil {
				log.Error("marshaling experiment %s - %v", name, err)
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			broker.Broadcast(
				broker.NewRequestPolicy("experiments", name, "get"),
				broker.NewResource("experiment", name, "start"),
				body,
			)

			w.Write(body)
			return
		default:
			p, err := mm.GetLaunchProgress(name, count)
			if err != nil {
				log.Error("getting progress for experiment %s - %v", name, err)
				continue
			}

			if p > progress {
				progress = p
			}

			log.Info("percent deployed: %v", progress*100.0)

			status := map[string]interface{}{
				"percent": progress,
			}

			marshalled, _ := json.Marshal(status)

			broker.Broadcast(
				broker.NewRequestPolicy("experiments", name, "get"),
				broker.NewResource("experiment", name, "progress"),
				marshalled,
			)

			time.Sleep(2 * time.Second)
		}
	}
}

// POST /experiments/{name}/stop
func StopExperiment(w http.ResponseWriter, r *http.Request) {
	log.Debug("StopExperiment HTTP handler called")

	var (
		ctx  = r.Context()
		role = ctx.Value("role").(rbac.Role)
		vars = mux.Vars(r)
		name = vars["name"]
	)

	if !role.Allowed("experiments/stop", name, "update") {
		log.Warn("stopping experiment %s not allowed for %s", name, ctx.Value("user").(string))
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if err := lockExperimentForStopping(name); err != nil {
		log.Warn(err.Error())
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	defer unlockExperiment(name)

	broker.Broadcast(
		broker.NewRequestPolicy("experiments", name, "get"),
		broker.NewResource("experiment", name, "stopping"),
		nil,
	)

	if err := experiment.Stop(name); err != nil {
		broker.Broadcast(
			broker.NewRequestPolicy("experiments", name, "get"),
			broker.NewResource("experiment", name, "errorStopping"),
			nil,
		)

		log.Error("stopping experiment %s - %v", name, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	exp, err := experiment.Get(name)
	if err != nil {
		// TODO
	}

	vms, err := vm.List(name)
	if err != nil {
		// TODO
	}

	body, err := json.Marshal(contract.NewExperiment(*exp, "", vms))
	if err != nil {
		log.Error("marshaling experiment %s - %v", name, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	broker.Broadcast(
		broker.NewRequestPolicy("experiments", name, "get"),
		broker.NewResource("experiment", name, "stop"),
		body,
	)

	w.Write(body)
}

// GET /experiments/{name}/schedule
func GetExperimentSchedule(w http.ResponseWriter, r *http.Request) {
	log.Debug("GetExperimentSchedule HTTP handler called")

	var (
		ctx  = r.Context()
		role = ctx.Value("role").(rbac.Role)
		vars = mux.Vars(r)
		name = vars["name"]
	)

	if !role.Allowed("experiments/schedule", name, "get") {
		log.Warn("getting experiment schedule for %s not allowed for %s", name, ctx.Value("user").(string))
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if status := isExperimentLocked(name); status != "" {
		msg := fmt.Sprintf("experiment %s is locked with status %s", name, status)

		log.Warn(msg)
		http.Error(w, msg, http.StatusConflict)

		return
	}

	exp, err := experiment.Get(name)
	if err != nil {
		log.Error("getting experiment %s - %v", name, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	body, err := json.Marshal(contract.NewExperimentSchedule(*exp))
	if err != nil {
		log.Error("marshaling schedule for experiment %s - %v", name, err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	w.Write(body)
}

// POST /experiments/{name}/schedule
func ScheduleExperiment(w http.ResponseWriter, r *http.Request) {
	log.Debug("ScheduleExperiment HTTP handler called")

	var (
		ctx  = r.Context()
		role = ctx.Value("role").(rbac.Role)
		vars = mux.Vars(r)
		name = vars["name"]
	)

	if !role.Allowed("experiments/schedule", name, "create") {
		log.Warn("creating experiment schedule for %s not allowed for %s", name, ctx.Value("user").(string))
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if status := isExperimentLocked(name); status != "" {
		msg := fmt.Sprintf("experiment %s is locked with status %s", name, status)

		log.Warn(msg)
		http.Error(w, msg, http.StatusConflict)

		return
	}

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		log.Error("reading request body - %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var req contract.UpdateExperimentScheduleRequest
	err = json.Unmarshal(body, &req)
	if err != nil {
		log.Error("unmarshaling request body - %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	err = experiment.Schedule(experiment.ScheduleForName(name), experiment.ScheduleWithAlgorithm(req.Algorithm))
	if err != nil {
		log.Error("scheduling experiment %s using %s - %v", name, req.Algorithm, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	exp, err := experiment.Get(name)
	if err != nil {
		log.Error("getting experiment %s - %v", name, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	body, err = json.Marshal(contract.NewExperimentSchedule(*exp))
	if err != nil {
		log.Error("marshaling schedule for experiment %s - %v", name, err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	broker.Broadcast(
		broker.NewRequestPolicy("experiments", name, "get"),
		broker.NewResource("experiment", name, "schedule"),
		body,
	)

	w.Write(body)
}

// GET /experiments/{name}/captures
func GetExperimentCaptures(w http.ResponseWriter, r *http.Request) {
	log.Debug("GetExperimentCaptures HTTP handler called")

	var (
		ctx  = r.Context()
		role = ctx.Value("role").(rbac.Role)
		vars = mux.Vars(r)
		name = vars["name"]
	)

	if !role.Allowed("experiments/captures", name, "list") {
		log.Warn("listing experiment captures for %s not allowed for %s", name, ctx.Value("user").(string))
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	var (
		captures = mm.GetExperimentCaptures(mm.NS(name))
		allowed  []mm.Capture
	)

	for _, capture := range captures {
		if role.Allowed("experiments/captures", name, "list", capture.VM) {
			allowed = append(allowed, capture)
		}
	}

	body, err := json.Marshal(util.WithRoot("captures", allowed))
	if err != nil {
		log.Error("marshaling captures for experiment %s - %v", name, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Write(body)
}

// GET /experiments/{name}/files
func GetExperimentFiles(w http.ResponseWriter, r *http.Request) {
	log.Debug("GetExperimentFiles HTTP handler called")

	var (
		ctx  = r.Context()
		role = ctx.Value("role").(rbac.Role)
		vars = mux.Vars(r)
		name = vars["name"]
	)

	if !role.Allowed("experiments/files", name, "list") {
		log.Warn("listing experiment files for %s not allowed for %s", name, ctx.Value("user").(string))
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	files, err := experiment.Files(name)
	if err != nil {
		log.Error("getting list of files for experiment %s - %v", name, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	body, err := json.Marshal(util.WithRoot("files", files))
	if err != nil {
		log.Error("marshaling file list for experiment %s - %v", name, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Write(body)
}

// GET /experiments/{name}/files/{filename}
func GetExperimentFile(w http.ResponseWriter, r *http.Request) {
	log.Debug("GetExperimentFile HTTP handler called")

	var (
		ctx  = r.Context()
		role = ctx.Value("role").(rbac.Role)
		vars = mux.Vars(r)
		name = vars["name"]
		file = vars["filename"]
	)

	if !role.Allowed("experiments/files", name, "get") {
		log.Warn("getting experiment file for %s not allowed for %s", name, ctx.Value("user").(string))
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	contents, err := experiment.File(name, file)
	if err != nil {
		log.Error("getting file %s for experiment %s - %v", file, name, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Disposition", "attachment; filename="+file)
	http.ServeContent(w, r, "", time.Now(), bytes.NewReader(contents))
}

// GET /experiments/{exp}/vms
func GetVMs(w http.ResponseWriter, r *http.Request) {
	log.Debug("GetVMs HTTP handler called")

	var (
		ctx     = r.Context()
		role    = ctx.Value("role").(rbac.Role)
		vars    = mux.Vars(r)
		exp     = vars["exp"]
		query   = r.URL.Query()
		size    = query.Get("screenshot")
		sortCol = query.Get("sortCol")
		sortDir = query.Get("sortDir")
		pageNum = query.Get("pageNum")
		perPage = query.Get("perPage")
	)

	if !role.Allowed("vms", exp, "list") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	vms, err := vm.List(exp)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	allowed := mm.VMs{}

	for _, vm := range vms {
		if role.Allowed("vms", exp, "list", vm.Name) {
			if vm.Running && size != "" {
				screenshot, err := util.GetScreenshot(exp, vm.Name, size)
				if err != nil {
					log.Error("getting screenshot: %v", err)
				} else {
					vm.Screenshot = "data:image/png;base64," + base64.StdEncoding.EncodeToString(screenshot)
				}
			}

			allowed = append(allowed, vm)
		}
	}

	if sortCol != "" && sortDir != "" {
		allowed.SortBy(sortCol, sortDir == "asc")
	}

	if pageNum != "" && perPage != "" {
		n, _ := strconv.Atoi(pageNum)
		s, _ := strconv.Atoi(perPage)

		allowed = allowed.Paginate(n, s)
	}

	body, err := json.Marshal(contract.NewVMList(allowed))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Write(body)
}

// GET /experiments/{exp}/vms/{name}
func GetVM(w http.ResponseWriter, r *http.Request) {
	log.Debug("GetVM HTTP handler called")

	var (
		ctx   = r.Context()
		role  = ctx.Value("role").(rbac.Role)
		vars  = mux.Vars(r)
		exp   = vars["exp"]
		name  = vars["name"]
		query = r.URL.Query()
		size  = query.Get("screenshot")
	)

	if !role.Allowed("vms", exp, "get", name) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	vm, err := vm.Get(exp, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if vm.Running && size != "" {
		screenshot, err := util.GetScreenshot(exp, name, size)
		if err != nil {
			log.Error("getting screenshot: %v", err)
		} else {
			vm.Screenshot = "data:image/png;base64," + base64.StdEncoding.EncodeToString(screenshot)
		}
	}

	body, err := json.Marshal(vm)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Write(body)
}

// PATCH /experiments/{exp}/vms/{name}
func UpdateVM(w http.ResponseWriter, r *http.Request) {
	log.Debug("UpdateVM HTTP handler called")
	var (
		ctx  = r.Context()
		role = ctx.Value("role").(rbac.Role)
		vars = mux.Vars(r)
		exp  = vars["exp"]
		name = vars["name"]
	)

	if !role.Allowed("vms", exp, "patch", name) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var req contract.UpdateVMRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	opts := []vm.UpdateOption{
		vm.UpdateExperiment(exp),
		vm.UpdateVM(name),
		vm.UpdateWithCPU(int(req.CPUs)),
		vm.UpdateWithMem(int(req.RAM)),
		vm.UpdateWithDisk(req.Disk),
	}

	if req.Interface != nil {
		opts = append(opts, vm.UpdateWithInterface(int(req.Interface.Index), req.Interface.VLAN))
	}

	if req.DoNotBoot != nil {
		opts = append(opts, vm.UpdateWithDNB(*req.DoNotBoot))
	}

	if req.Host != nil {
		opts = append(opts, vm.UpdateWithHost(*req.Host))
	}

	if err := vm.Update(opts...); err != nil {
		log.Error("updating VM: %v", err)
		http.Error(w, "unable to update VM", http.StatusInternalServerError)
		return
	}

	vm, err := vm.Get(exp, name)
	if err != nil {
		http.Error(w, "unable to get VM", http.StatusInternalServerError)
		return
	}

	if vm.Running {
		screenshot, err := util.GetScreenshot(exp, name, "215")
		if err != nil {
			log.Error("getting screenshot: %v", err)
		} else {
			vm.Screenshot = "data:image/png;base64," + base64.StdEncoding.EncodeToString(screenshot)
		}
	}

	body, err = json.Marshal(vm)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	broker.Broadcast(
		broker.NewRequestPolicy("vms", exp, "get", name),
		broker.NewResource("experiment/vm", fmt.Sprintf("%s/%s", exp, name), "update"),
		body,
	)

	w.Write(body)
}

// DELETE /experiments/{exp}/vms/{name}
func DeleteVM(w http.ResponseWriter, r *http.Request) {
	log.Debug("DeleteVM HTTP handler called")

	var (
		ctx  = r.Context()
		role = ctx.Value("role").(rbac.Role)
		vars = mux.Vars(r)
		exp  = vars["exp"]
		name = vars["name"]
	)

	if !role.Allowed("vms", exp, "delete", name) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	e, err := experiment.Get(exp)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if !e.Status.Running() {
		http.Error(w, "experiment not running", http.StatusBadRequest)
		return
	}

	if err := mm.KillVM(mm.NS(exp), mm.VMName(name)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	broker.Broadcast(
		broker.NewRequestPolicy("vms", exp, "get", name),
		broker.NewResource("experiment/vm", fmt.Sprintf("%s/%s", exp, name), "delete"),
		nil,
	)

	w.WriteHeader(http.StatusNoContent)
}

// POST /experiments/{exp}/vms/{name}/start
func StartVM(w http.ResponseWriter, r *http.Request) {
	log.Debug("StartVM HTTP handler called")

	var (
		ctx  = r.Context()
		role = ctx.Value("role").(rbac.Role)
		vars = mux.Vars(r)
		exp  = vars["exp"]
		name = vars["name"]
	)

	if !role.Allowed("vms/start", exp, "update", name) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if err := lockVMForStarting(exp, name); err != nil {
		log.Warn(err.Error())
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	defer unlockVM(exp, name)

	broker.Broadcast(
		broker.NewRequestPolicy("vms", exp, "get", name),
		broker.NewResource("experiment/vm", fmt.Sprintf("%s/%s", exp, name), "starting"),
		nil,
	)

	if err := mm.StartVM(mm.NS(exp), mm.VMName(name)); err != nil {
		broker.Broadcast(
			broker.NewRequestPolicy("vms", exp, "get", name),
			broker.NewResource("experiment/vm", fmt.Sprintf("%s/%s", exp, name), "errorStarting"),
			nil,
		)

		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	v, err := vm.Get(exp, name)
	if err != nil {
		broker.Broadcast(
			broker.NewRequestPolicy("vms", exp, "get", name),
			broker.NewResource("experiment/vm", fmt.Sprintf("%s/%s", exp, name), "errorStarting"),
			nil,
		)

		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	screenshot, err := util.GetScreenshot(exp, name, "215")
	if err != nil {
		log.Error("getting screenshot - %v", err)
	} else {
		v.Screenshot = "data:image/png;base64," + base64.StdEncoding.EncodeToString(screenshot)
	}

	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	broker.Broadcast(
		broker.NewRequestPolicy("vms", exp, "get", name),
		broker.NewResource("experiment/vm", fmt.Sprintf("%s/%s", exp, name), "start"),
		body,
	)

	w.Write(body)
}

// POST /experiments/{exp}/vms/{name}/stop
func StopVM(w http.ResponseWriter, r *http.Request) {
	log.Debug("StopVM HTTP handler called")

	var (
		ctx  = r.Context()
		role = ctx.Value("role").(rbac.Role)
		vars = mux.Vars(r)
		exp  = vars["exp"]
		name = vars["name"]
	)

	if !role.Allowed("vms/stop", exp, "update", name) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if err := lockVMForStopping(exp, name); err != nil {
		log.Warn(err.Error())
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	defer unlockVM(exp, name)

	broker.Broadcast(
		broker.NewRequestPolicy("vms", exp, "get", name),
		broker.NewResource("experiment/vm", fmt.Sprintf("%s/%s", exp, name), "stopping"),
		nil,
	)

	if err := mm.StopVM(mm.NS(exp), mm.VMName(name)); err != nil {
		broker.Broadcast(
			broker.NewRequestPolicy("vms", exp, "get", name),
			broker.NewResource("experiment/vm", fmt.Sprintf("%s/%s", exp, name), "errorStopping"),
			nil,
		)

		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	v, err := vm.Get(exp, name)
	if err != nil {
		broker.Broadcast(
			broker.NewRequestPolicy("vms", exp, "get", name),
			broker.NewResource("experiment/vm", fmt.Sprintf("%s/%s", exp, name), "errorStopping"),
			nil,
		)

		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	broker.Broadcast(
		broker.NewRequestPolicy("vms", exp, "get", name),
		broker.NewResource("experiment/vm", fmt.Sprintf("%s/%s", exp, name), "stop"),
		body,
	)

	w.Write(body)
}

// POST /experiments/{exp}/vms/{name}/redeploy
func RedeployVM(w http.ResponseWriter, r *http.Request) {
	log.Debug("RedeployVM HTTP handler called")

	var (
		ctx    = r.Context()
		role   = ctx.Value("role").(rbac.Role)
		vars   = mux.Vars(r)
		exp    = vars["exp"]
		name   = vars["name"]
		query  = r.URL.Query()
		inject = query.Get("replicate-injects") != ""
	)

	if !role.Allowed("vms/redeploy", exp, "update", name) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if err := lockVMForRedeploying(exp, name); err != nil {
		log.Warn(err.Error())
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	defer unlockVM(exp, name)

	v, err := vm.Get(exp, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	v.Redeploying = true

	body, _ := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	broker.Broadcast(
		broker.NewRequestPolicy("vms", exp, "get", name),
		broker.NewResource("experiment/vm", fmt.Sprintf("%s/%s", exp, name), "redeploying"),
		body,
	)

	redeployed := make(chan error)

	go func() {
		defer close(redeployed)

		body, err := ioutil.ReadAll(r.Body)
		if err != nil && err != io.EOF {
			redeployed <- err
			return
		}

		opts := []vm.RedeployOption{
			vm.CPU(v.CPUs),
			vm.Memory(v.RAM),
			vm.Disk(v.Disk),
			vm.Inject(inject),
		}

		// `body` will be nil if err above was EOF.
		if body != nil {
			var req contract.VMRedeployRequest

			// Update VM struct with values from POST request body.
			if err := json.Unmarshal(body, &req); err != nil {
				redeployed <- err
				return
			}

			opts = []vm.RedeployOption{
				vm.CPU(int(req.CPUs)),
				vm.Memory(int(req.RAM)),
				vm.Disk(req.Disk),
				vm.Inject(req.Injects),
			}
		}

		if err := vm.Redeploy(exp, name, opts...); err != nil {
			redeployed <- err
		}

		v.Redeploying = false
	}()

	// HACK: mandatory sleep time to make it seem like a redeploy is
	// happening client-side, even when the redeploy is fast (like for
	// Linux VMs).
	time.Sleep(5 * time.Second)

	err = <-redeployed
	if err != nil {
		log.Error("redeploying VM %s - %v", fmt.Sprintf("%s/%s", exp, name), err)

		broker.Broadcast(
			broker.NewRequestPolicy("vms", exp, "get", name),
			broker.NewResource("experiment/vm", fmt.Sprintf("%s/%s", exp, name), "errorRedeploying"),
			nil,
		)

		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Get the VM details again since redeploying may have changed them.
	v, err = vm.Get(exp, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	screenshot, err := util.GetScreenshot(exp, name, "215")
	if err != nil {
		log.Error("getting screenshot - %v", err)
	} else {
		v.Screenshot = "data:image/png;base64," + base64.StdEncoding.EncodeToString(screenshot)
	}

	body, _ = json.Marshal(v)

	broker.Broadcast(
		broker.NewRequestPolicy("vms", exp, "get", name),
		broker.NewResource("experiment/vm", fmt.Sprintf("%s/%s", exp, name), "redeployed"),
		body,
	)

	w.Write(body)
}

// GET /experiments/{exp}/vms/{name}/screenshot.png
func GetScreenshot(w http.ResponseWriter, r *http.Request) {
	log.Debug("GetScreenshot HTTP handler called")

	var (
		ctx    = r.Context()
		role   = ctx.Value("role").(rbac.Role)
		vars   = mux.Vars(r)
		exp    = vars["exp"]
		name   = vars["name"]
		query  = r.URL.Query()
		size   = query.Get("size")
		encode = query.Get("base64") != ""
	)

	if !role.Allowed("vms/screenshot", exp, "get", name) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if size == "" {
		size = "215"
	}

	screenshot, err := util.GetScreenshot(exp, name, size)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if encode {
		encoded := "data:image/png;base64," + base64.StdEncoding.EncodeToString(screenshot)
		w.Write([]byte(encoded))
		return
	}

	w.Header().Set("Content-Type", "image/png")
	w.Write(screenshot)
}

// GET /experiments/{exp}/vms/{name}/vnc
func GetVNC(w http.ResponseWriter, r *http.Request) {
	log.Debug("GetVNC HTTP handler called")

	var (
		ctx  = r.Context()
		role = ctx.Value("role").(rbac.Role)
		vars = mux.Vars(r)
		exp  = vars["exp"]
		name = vars["name"]
	)

	if !role.Allowed("vms/vnc", exp, "get", name) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	bfs := util.NewBinaryFileSystem(
		&assetfs.AssetFS{
			Asset:     Asset,
			AssetDir:  AssetDir,
			AssetInfo: AssetInfo,
		},
	)

	// set no-cache headers
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate") // HTTP 1.1.
	w.Header().Set("Pragma", "no-cache")                                   // HTTP 1.0.
	w.Header().Set("Expires", "0")                                         // Proxies.

	bfs.ServeFile(w, r, "vnc.html")
}

// GET /experiments/{exp}/vms/{name}/vnc/ws
func GetVNCWebSocket(w http.ResponseWriter, r *http.Request) {
	log.Debug("GetVNCWebSocket HTTP handler called")

	var (
		vars = mux.Vars(r)
		exp  = vars["exp"]
		name = vars["name"]
	)

	endpoint, err := mm.GetVNCEndpoint(mm.NS(exp), mm.VMName(name))
	if err != nil {
		log.Error("getting VNC endpoint: %v", err)
		http.Error(w, "", http.StatusBadRequest)
		return
	}

	websocket.Handler(util.ConnectWSHandler(endpoint)).ServeHTTP(w, r)
}

// GET /experiments/{exp}/vms/{name}/captures
func GetVMCaptures(w http.ResponseWriter, r *http.Request) {
	log.Debug("GetVMCaptures HTTP handler called")

	var (
		ctx  = r.Context()
		role = ctx.Value("role").(rbac.Role)
		vars = mux.Vars(r)
		exp  = vars["exp"]
		name = vars["name"]
	)

	if !role.Allowed("vms/captures", exp, "list", name) {
		log.Warn("getting captures for VM %s in experiment %s not allowed for %s", name, exp, ctx.Value("user").(string))
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	captures := mm.GetVMCaptures(mm.NS(exp), mm.VMName(name))

	body, err := json.Marshal(util.WithRoot("captures", captures))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Write(body)
}

// POST /experiments/{exp}/vms/{name}/captures
func StartVMCapture(w http.ResponseWriter, r *http.Request) {
	log.Debug("StartVMCapture HTTP handler called")

	var (
		ctx  = r.Context()
		role = ctx.Value("role").(rbac.Role)
		vars = mux.Vars(r)
		exp  = vars["exp"]
		name = vars["name"]
	)

	if !role.Allowed("vms/captures", exp, "create", name) {
		log.Warn("starting capture for VM %s in experiment %s not allowed for %s", name, exp, ctx.Value("user").(string))
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		log.Error("reading request body - %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var req contract.StartCaptureRequest
	err = json.Unmarshal(body, &req)
	if err != nil {
		log.Error("unmarshaling request body - %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := mm.StartVMCapture(mm.NS(exp), mm.VMName(name), mm.CaptureInterface(int(req.Interface)), mm.CaptureFile(req.Filename)); err != nil {
		log.Error("starting VM capture for VM %s in experiment %s - %v", name, exp, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	broker.Broadcast(
		broker.NewRequestPolicy("vms", exp, "get", name),
		broker.NewResource("experiment/vm/capture", fmt.Sprintf("%s/%s", exp, name), "start"),
		body,
	)

	w.WriteHeader(http.StatusNoContent)
}

// DELETE /experiments/{exp}/vms/{name}/captures
func StopVMCaptures(w http.ResponseWriter, r *http.Request) {
	log.Debug("StopVMCaptures HTTP handler called")

	var (
		ctx  = r.Context()
		role = ctx.Value("role").(rbac.Role)
		vars = mux.Vars(r)
		exp  = vars["exp"]
		name = vars["name"]
	)

	if !role.Allowed("vms/captures", exp, "delete", name) {
		log.Warn("stopping capture for VM %s in experiment %s not allowed for %s", name, exp, ctx.Value("user").(string))
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	err := mm.StopVMCapture(mm.NS(exp), mm.VMName(name))
	if err != nil && err != mm.ErrNoCaptures {
		log.Error("stopping VM capture for VM %s in experiment %s - %v", name, exp, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	broker.Broadcast(
		broker.NewRequestPolicy("vms", exp, "get", name),
		broker.NewResource("experiment/vm/capture", fmt.Sprintf("%s/%s", exp, name), "stop"),
		nil,
	)

	w.WriteHeader(http.StatusNoContent)
}

// GET /experiments/{exp}/vms/{name}/snapshots
func GetVMSnapshots(w http.ResponseWriter, r *http.Request) {
	log.Debug("GetVMSnapshots HTTP handler called")

	var (
		ctx  = r.Context()
		role = ctx.Value("role").(rbac.Role)
		vars = mux.Vars(r)
		exp  = vars["exp"]
		name = vars["name"]
	)

	if !role.Allowed("vms/snapshots", exp, "list", name) {
		log.Warn("listing snapshots for VM %s in experiment %s not allowed for %s", name, exp, ctx.Value("user").(string))
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	snapshots, err := vm.Snapshots(exp, name)
	if err != nil {
		log.Error("getting list of snapshots for VM %s in experiment %s: %v", name, exp, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	body, err := json.Marshal(util.WithRoot("snapshots", snapshots))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Write(body)
}

// POST /experiments/{exp}/vms/{name}/snapshots
func SnapshotVM(w http.ResponseWriter, r *http.Request) {
	log.Debug("SnapshotVM HTTP handler called")

	var (
		ctx  = r.Context()
		role = ctx.Value("role").(rbac.Role)
		vars = mux.Vars(r)
		exp  = vars["exp"]
		name = vars["name"]
	)

	if !role.Allowed("vms/snapshots", exp, "create", name) {
		log.Warn("snapshotting VM %s in experiment %s not allowed for %s", name, exp, ctx.Value("user").(string))
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		log.Error("reading request body - %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var req contract.SnapshotRequest
	err = json.Unmarshal(body, &req)
	if err != nil {
		log.Error("unmarshaling request body - %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := lockVMForSnapshotting(exp, name); err != nil {
		log.Warn(err.Error())
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	defer unlockVM(exp, name)

	broker.Broadcast(
		broker.NewRequestPolicy("vms", exp, "get", name),
		broker.NewResource("experiment/vm/snapshot", fmt.Sprintf("%s/%s", exp, name), "creating"),
		nil,
	)

	status := make(chan string)

	go func() {
		for {
			s := <-status

			if s == "completed" {
				return
			}

			progress, err := strconv.ParseFloat(s, 64)
			if err == nil {
				log.Info("snapshot percent complete: %v", progress)

				status := map[string]interface{}{
					"percent": progress / 100,
				}

				marshalled, _ := json.Marshal(status)

				broker.Broadcast(
					broker.NewRequestPolicy("vms", exp, "get", name),
					broker.NewResource("experiment/vm/snapshot", fmt.Sprintf("%s/%s", exp, name), "progress"),
					marshalled,
				)
			}
		}
	}()

	cb := func(s string) { status <- s }

	if err := vm.Snapshot(exp, name, req.Filename, cb); err != nil {
		broker.Broadcast(
			broker.NewRequestPolicy("vms", exp, "get", name),
			broker.NewResource("experiment/vm/snapshot", fmt.Sprintf("%s/%s", exp, name), "errorCreating"),
			nil,
		)

		log.Error("snapshotting VM %s in experiment %s - %v", name, exp, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	broker.Broadcast(
		broker.NewRequestPolicy("vms", exp, "get", name),
		broker.NewResource("experiment/vm/snapshot", fmt.Sprintf("%s/%s", exp, name), "create"),
		nil,
	)

	w.WriteHeader(http.StatusNoContent)
}

// POST /experiments/{exp}/vms/{name}/snapshots/{snapshot}
func RestoreVM(w http.ResponseWriter, r *http.Request) {
	log.Debug("RestoreVM HTTP handler called")

	var (
		ctx  = r.Context()
		role = ctx.Value("role").(rbac.Role)
		vars = mux.Vars(r)
		exp  = vars["exp"]
		name = vars["name"]
		snap = vars["snapshot"]
	)

	if !role.Allowed("vms/snapshots", exp, "update", name) {
		log.Warn("restoring VM %s in experiment %s not allowed for %s", name, exp, ctx.Value("user").(string))
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if err := lockVMForRestoring(exp, name); err != nil {
		log.Warn(err.Error())
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	defer unlockVM(exp, name)

	broker.Broadcast(
		broker.NewRequestPolicy("vms", exp, "get", name),
		broker.NewResource("experiment/vm/snapshot", fmt.Sprintf("%s/%s", exp, name), "restoring"),
		nil,
	)

	if err := vm.Restore(exp, name, snap); err != nil {
		broker.Broadcast(
			broker.NewRequestPolicy("vms", exp, "get", name),
			broker.NewResource("experiment/vm/snapshot", fmt.Sprintf("%s/%s", exp, name), "errorRestoring"),
			nil,
		)

		log.Error("restoring VM %s in experiment %s - %v", name, exp, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	broker.Broadcast(
		broker.NewRequestPolicy("vms", exp, "get", name),
		broker.NewResource("experiment/vm/snapshot", fmt.Sprintf("%s/%s", exp, name), "restore"),
		nil,
	)

	w.WriteHeader(http.StatusNoContent)
}

// POST /experiments/{exp}/vms/{name}/commit
func CommitVM(w http.ResponseWriter, r *http.Request) {
	log.Debug("CommitVM HTTP handler called")

	var (
		ctx  = r.Context()
		role = ctx.Value("role").(rbac.Role)
		vars = mux.Vars(r)
		exp  = vars["exp"]
		name = vars["name"]
	)

	if !role.Allowed("vms/commit", exp, "create", name) {
		log.Warn("committing VM %s in experiment %s not allowed for %s", name, exp, ctx.Value("user").(string))
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		log.Error("reading request body - %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var filename string

	// If user provided body to this request, expect it to specify the
	// filename to use for the commit. If no body was provided, pass an
	// empty string to `api.CommitToDisk` to let it create a copy based on
	// the existing file name for the base image.
	if len(body) != 0 {
		var req contract.BackingImageRequest
		err = json.Unmarshal(body, &req)
		if err != nil {
			log.Error("unmarshaling request body - %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if req.Filename == "" {
			log.Error("missing filename for commit")
			http.Error(w, "missing 'filename' key", http.StatusBadRequest)
			return
		}

		filename = req.Filename
	}

	if err := lockVMForCommitting(exp, name); err != nil {
		log.Warn(err.Error())
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	defer unlockVM(exp, name)

	if filename == "" {
		/*
			if filename, err = api.GetNewDiskName(exp, name); err != nil {
				log.Error("failure getting new disk name for commit")
				http.Error(w, "failure getting new disk name for commit", http.StatusInternalServerError)
				return
			}
		*/

		// TODO

		http.Error(w, "must provide new disk name for commit", http.StatusBadRequest)
		return
	}

	payload := contract.NewBackingImageResponse(filename, nil)
	body, _ = json.Marshal(payload)

	broker.Broadcast(
		broker.NewRequestPolicy("vms", exp, "get", name),
		broker.NewResource("experiment/vm/commit", fmt.Sprintf("%s/%s", exp, name), "committing"),
		body,
	)

	status := make(chan float64)

	go func() {
		for s := range status {
			log.Info("VM commit percent complete: %v", s)

			status := map[string]interface{}{
				"percent": s,
			}

			marshalled, _ := json.Marshal(status)

			broker.Broadcast(
				broker.NewRequestPolicy("vms", exp, "get", name),
				broker.NewResource("experiment/vm/commit", fmt.Sprintf("%s/%s", exp, name), "progress"),
				marshalled,
			)
		}
	}()

	cb := func(s float64) { status <- s }

	if filename, err = vm.CommitToDisk(exp, name, filename, cb); err != nil {
		broker.Broadcast(
			broker.NewRequestPolicy("vms", exp, "get", name),
			broker.NewResource("experiment/vm/commit", fmt.Sprintf("%s/%s", exp, name), "errorCommitting"),
			nil,
		)

		log.Error("committing VM %s in experiment %s - %v", name, exp, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	v, err := vm.Get(exp, name)
	if err != nil {
		broker.Broadcast(
			broker.NewRequestPolicy("vms", exp, "get", name),
			broker.NewResource("experiment/vm/commit", fmt.Sprintf("%s/%s", exp, name), "errorCommitting"),
			nil,
		)

		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	payload.VM = v
	body, _ = json.Marshal(payload)

	broker.Broadcast(
		broker.NewRequestPolicy("vms", exp, "get", name),
		broker.NewResource("experiment/vm/commit", fmt.Sprintf("%s/%s", exp, name), "commit"),
		body,
	)

	w.Write(body)
}

// GET /vms
func GetAllVMs(w http.ResponseWriter, r *http.Request) {
	log.Debug("GetAllVMs HTTP handler called")

	var (
		ctx   = r.Context()
		role  = ctx.Value("role").(rbac.Role)
		query = r.URL.Query()
		size  = query.Get("screenshot")
	)

	if !role.Allowed("vms", "", "list") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	exps, err := experiment.List()
	if err != nil {
		log.Error("getting experiments: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	allowed := []mm.VM{}

	for _, exp := range exps {
		vms, err := vm.List(exp.Spec.ExperimentName)
		if err != nil {
			// TODO
		}

		for _, v := range vms {
			if !role.Allowed("vms", exp.Spec.ExperimentName(), "list", v.Name) {
				continue
			}

			// TODO: add `show_dnb` query option.
			if !v.Running {
				continue
			}

			if size != "" {
				screenshot, err := util.GetScreenshot(exp.Spec.ExperimentName, v.Name, size)
				if err != nil {
					log.Error("getting screenshot: %v", err)
				} else {
					v.Screenshot = "data:image/png;base64," + base64.StdEncoding.EncodeToString(screenshot)
				}
			}

			allowed = append(allowed, v)
		}
	}

	body, err := json.Marshal(contract.NewVMList(allowed))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Write(body)
}

// GET /topologies
func GetTopologies(w http.ResponseWriter, r *http.Request) {
	log.Debug("GetTopologies HTTP handler called")

	var (
		ctx  = r.Context()
		role = ctx.Value("role").(rbac.Role)
	)

	if !role.Allowed("topologies", "", "list") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	topologies, err := config.List("topology")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	allowed := []string{}
	for _, topo := range topologies {
		if role.Allowed("topologies", "", "list", topo.Metadata.Name) {
			allowed = append(allowed, topo.Metadata.Name)
		}
	}

	body, err := json.Marshal(util.WithRoot("topologies", allowed))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Write(body)
}

// GET /topologies/{topo}/scenarios
func GetScenarios(w http.ResponseWriter, r *http.Request) {
	log.Debug("GetScenarios HTTP handler called")

	var (
		ctx  = r.Context()
		role = ctx.Value("role").(rbac.Role)
		vars = mux.Vars(r)
		topo = vars["topo"]
	)

	if !role.Allowed("scenarios", "", "list") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	scenarios, err := config.List("scenario")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	allowed := make(map[string][]string)

	for _, s := range scenarios {
		// We only care about scenarios pertaining to the given topology.
		if t := s.Metadata.Annotations["topology"]; t != topo {
			continue
		}

		if role.Allowed("scenarios", "", "list", s.Metadata.Name) {
			apps, err := scenario.AppList(s.Metadata.Name)
			if err != nil {
				log.Error("getting apps for scenario %s: %v", s.Metadata.Name, err)
				continue
			}

			list := make([]string, len(apps))
			for i, a := range apps {
				list[i] = a
			}

			allowed[s.Metadata.Name] = list
		}
	}

	body, err := json.Marshal(util.WithRoot("scenarios", allowed))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Write(body)
}

// GET /disks
func GetDisks(w http.ResponseWriter, r *http.Request) {
	log.Debug("GetDisks HTTP handler called")

	var (
		ctx  = r.Context()
		role = ctx.Value("role").(rbac.Role)
	)

	if !role.Allowed("disks", "", "list") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	disks, err := cluster.GetImages(cluster.VM_IMAGE)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	allowed := []string{}
	for _, disk := range disks {
		if role.Allowed("disks", "", "list", disk.Name) {
			allowed = append(allowed, disk.Name)
		}
	}

	body, err := json.Marshal(util.WithRoot("disks", allowed))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Write(body)
}

// GET /hosts
func GetClusterHosts(w http.ResponseWriter, r *http.Request) {
	log.Debug("GetClusterHosts HTTP handler called")

	var (
		ctx  = r.Context()
		role = ctx.Value("role").(rbac.Role)
	)

	if !role.Allowed("hosts", "", "list") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	hosts, err := mm.GetClusterHosts()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	allowed := []mm.Host{}
	for _, host := range hosts {
		if role.Allowed("hosts", "", "list", host.Name) {
			allowed = append(allowed, host)
		}
	}

	marshalled, err := json.Marshal(mm.Cluster{Hosts: allowed})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Write(marshalled)
}

// GET /logs
func GetLogs(w http.ResponseWriter, r *http.Request) {
	if !o.publishLogs {
		w.WriteHeader(http.StatusNotImplemented)
	}

	type LogLine struct {
		Source    string `json:"source"`
		Timestamp string `json:"timestamp"`
		Level     string `json:"level"`
		Log       string `json:"log"`

		// Not exported so it doesn't get included in serialized JSON.
		ts time.Time
	}

	var (
		since time.Duration
		limit int

		logs    = make(map[int][]LogLine)
		logChan = make(chan LogLine)
		done    = make(chan struct{})
		wait    errgroup.Group

		logFiles = map[string]string{
			"minimega": o.minimegaLogs,
			"phenix":   o.phenixLogs,
		}
	)

	// If no since duration is provided, or the value provided is not a
	// valid duration string, since will default to 1h.
	if err := parseDuration(r.URL.Query().Get("since"), &since); err != nil {
		since = 1 * time.Hour
	}

	// If no limit is provided, or the value provided is not an int, limit
	// will default to 0.
	parseInt(r.URL.Query().Get("limit"), &limit)

	go func() {
		for l := range logChan {
			ts := int(l.ts.Unix())

			tl := logs[ts]
			tl = append(tl, l)

			logs[ts] = tl
		}

		close(done)
	}()

	for k := range logFiles {
		name := k
		path := logFiles[k]

		wait.Go(func() error {
			f, err := os.Open(path)
			if err != nil {
				// This *most likely* means the log file doesn't exist yet, so just exit
				// out of the Goroutine cleanly.
				return nil
			}

			defer f.Close()

			var (
				scanner = bufio.NewScanner(f)
				// Used to detect multi-line logs in tailed log files.
				body *LogLine
			)

			for scanner.Scan() {
				parts := logLineRegex.FindStringSubmatch(scanner.Text())

				if len(parts) == 4 {
					ts, err := time.ParseInLocation("2006/01/02 15:04:05", parts[1], time.Local)
					if err != nil {
						continue
					}

					if time.Now().Sub(ts) > since {
						continue
					}

					if parts[2] == "WARNING" {
						parts[2] = "WARN"
					}

					body = &LogLine{
						Source:    name,
						Timestamp: parts[1],
						Level:     parts[2],
						Log:       parts[3],

						ts: ts,
					}
				} else if body != nil {
					body.Log = scanner.Text()
				} else {
					continue
				}

				logChan <- *body
			}

			if err := scanner.Err(); err != nil {
				return errors.WithMessagef(err, "scanning %s log file at %s", name, path)
			}

			return nil
		})
	}

	if err := wait.Wait(); err != nil {
		http.Error(w, "error reading logs", http.StatusInternalServerError)
		return
	}

	// Close log channel, marking it as done.
	close(logChan)
	// Wait for Goroutine processing logs from log channel to be done.
	<-done

	var (
		idx, offset int
		ts          = make([]int, len(logs))
		limited     []LogLine
	)

	// Put log timestamps into slice so they can be sorted.
	for k := range logs {
		ts[idx] = k
		idx++
	}

	// Sort log timestamps.
	sort.Ints(ts)

	// Determine if final log slice should be limited.
	if limit != 0 && limit < len(ts) {
		offset = len(ts) - limit
	}

	// Loop through sorted, limited log timestamps and grab actual logs
	// for given timestamp.
	for _, k := range ts[offset:] {
		limited = append(limited, logs[k]...)
	}

	marshalled, _ := json.Marshal(util.WithRoot("logs", limited))
	w.Write(marshalled)
}

// GET /users
func GetUsers(w http.ResponseWriter, r *http.Request) {
	log.Debug("GetUsers HTTP handler called")

	var (
		ctx  = r.Context()
		role = ctx.Value("role").(rbac.Role)
	)

	if !role.Allowed("users", "", "list") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	users, err := rbac.GetUsers()
	if err != nil {
		http.Error(w, "", http.StatusInternalServerError)
		return
	}

	var resp []*v1.UserSpec

	for _, u := range users {
		spec := u.Spec

		if role.Allowed("users", "", "list", spec.Username) {
			spec.Password = ""
			spec.Tokens = nil

			resp = append(resp, spec)
		}
	}

	body, err := json.Marshal(util.WithRoot("users", resp))
	if err != nil {
		log.Error("marshaling users: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Write(body)
}

// POST /users
func CreateUser(w http.ResponseWriter, r *http.Request) {
	log.Debug("CreateUser HTTP handler called")

	var (
		ctx  = r.Context()
		role = ctx.Value("role").(rbac.Role)
	)

	if !role.Allowed("users", "", "create") {
		log.Warn("creating users not allowed for %s", ctx.Value("user").(string))
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		log.Error("reading request body - %v", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	contract := struct {
		Username      string   `json:"username"`
		Password      string   `json:"password"`
		FirstName     string   `json:"first_name"`
		LastName      string   `json:"last_name"`
		RoleName      string   `json:"role_name"`
		Experiments   []string `json:"experiments"`
		ResourceNames []string `json:"resource_names"`
	}{}

	if err := json.Unmarshal(body, &contract); err != nil {
		log.Error("unmashaling request body - %v", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	user := rbac.NewUser(contract.Username, contract.Password)

	user.Spec.FirstName = contract.FirstName
	user.Spec.LastName = contract.LastName

	uRole, err := rbac.RoleFromConfig(contract.RoleName)
	if err != nil {
		log.Error("role name not found - %s", contract.RoleName)
		http.Error(w, "role not found", http.StatusBadRequest)
		return
	}

	uRole.SetExperiments(contract.Experiments...)
	uRole.SetResourceNames(contract.ResourceNames...)

	// allow user to get their own user details
	uRole.AddPolicy(
		[]string{"users"},
		nil,
		[]string{"get"},
		[]string{contract.Username},
	)

	user.SetRole(uRole)

	user.Spec.Password = ""
	user.Spec.Tokens = nil

	body, err = json.Marshal(user.Spec)
	if err != nil {
		log.Error("marshaling user %s: %v", user.Username, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	broker.Broadcast(
		broker.NewRequestPolicy("users", "", "get"),
		broker.NewResource("user", user.Username(), "create"),
		body,
	)

	w.Write(body)
}

// GET /users/{username}
func GetUser(w http.ResponseWriter, r *http.Request) {
	log.Debug("GetUser HTTP handler called")

	var (
		ctx   = r.Context()
		role  = ctx.Value("role").(rbac.Role)
		vars  = mux.Vars(r)
		uname = vars["username"]
	)

	if !role.Allowed("users", "", "get", uname) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	user, err := rbac.GetUser(uname)
	if err != nil {
		http.Error(w, "unable to get user", http.StatusInternalServerError)
		return
	}

	user.Spec.Password = ""
	user.Spec.Tokens = nil

	body, err := json.Marshal(user.Spec)
	if err != nil {
		log.Error("marshaling user %s: %v", user.Username(), err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Write(body)
}

// PATCH /users/{username}
func UpdateUser(w http.ResponseWriter, r *http.Request) {
	log.Debug("UpdateUser HTTP handler called")

	var (
		ctx   = r.Context()
		role  = ctx.Value("role").(rbac.Role)
		vars  = mux.Vars(r)
		uname = vars["username"]
	)

	if !role.Allowed("users", "", "patch", uname) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	contract := struct {
		Username      string   `json:"username"`
		FirstName     string   `json:"first_name"`
		LastName      string   `json:"last_name"`
		RoleName      string   `json:"role_name"`
		Experiments   []string `json:"experiments"`
		ResourceNames []string `json:"resource_names"`
	}{}

	if err := json.Unmarshal(body, &contract); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	u, err := rbac.GetUser(uname)
	if err != nil {
		http.Error(w, "unable to get user", http.StatusInternalServerError)
		return
	}

	if contract.FirstName != "" {
		if err := u.UpdateFirstName(contract.FirstName); err != nil {
			log.Error("updating first name for user %s: %v", uname, err)
			http.Error(w, "unable to update user", http.StatusInternalServerError)
			return
		}
	}

	if contract.LastName != "" {
		if err := u.UpdateLastName(contract.LastName); err != nil {
			log.Error("updating last name for user %s: %v", uname, err)
			http.Error(w, "unable to update user", http.StatusInternalServerError)
			return
		}
	}

	if contract.RoleName != "" {
		uRole, err := rbac.RoleFromConfig(contract.RoleName)
		if err != nil {
			log.Error("role name not found - %s", contract.RoleName)
			http.Error(w, "role not found", http.StatusBadRequest)
			return
		}

		uRole.SetExperiments(contract.Experiments...)
		uRole.SetResourceNames(contract.ResourceNames...)

		// allow user to get their own user details
		uRole.AddPolicy(
			[]string{"users"},
			nil,
			[]string{"get"},
			[]string{uname},
		)

		u.SetRole(uRole)
	}

	u.Spec.Password = ""
	u.Spec.Tokens = nil

	body, err = json.Marshal(u.Spec)
	if err != nil {
		log.Error("marshaling user %s: %v", uname, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	broker.Broadcast(
		broker.NewRequestPolicy("users", "", "get", uname),
		broker.NewResource("user", uname, "update"),
		body,
	)

	w.Write(body)
}

// DELETE /users/{username}
func DeleteUser(w http.ResponseWriter, r *http.Request) {
	log.Debug("DeleteUser HTTP handler called")

	var (
		ctx   = r.Context()
		user  = ctx.Value("user").(string)
		role  = ctx.Value("role").(rbac.Role)
		vars  = mux.Vars(r)
		uname = vars["username"]
	)

	if user == uname {
		http.Error(w, "you cannot delete your own user", http.StatusForbidden)
		return
	}

	if !role.Allowed("users", "", "delete", uname) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if err := config.Delete("user/" + uname); err != nil {
		log.Error("deleting user %s: %v", uname, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	broker.Broadcast(
		broker.NewRequestPolicy("users", "", "get", uname),
		broker.NewResource("user", uname, "delete"),
		nil,
	)

	w.WriteHeader(http.StatusNoContent)
}

func Signup(w http.ResponseWriter, r *http.Request) {
	log.Debug("Signup HTTP handler called")

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		log.Error("reading request body - %v", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	contract := struct {
		Username  string `json:"username"`
		Password  string `json:"password"`
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
	}{}

	if err := json.Unmarshal(body, &contract); err != nil {
		log.Error("unmashaling request body - %v", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	u := rbac.NewUser(contract.Username, contract.Password)

	u.Spec.FirstName = contract.FirstName
	u.Spec.LastName = contract.LastName

	if err := u.Save(); err != nil {
		log.Error("saving user %s: %v", contract.Username, err)
		http.Error(w, "unable to create user", http.StatusInternalServerError)
		return
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": u.Username(),
		"exp": time.Now().Add(24 * time.Hour).Unix(),
	})

	// Sign and get the complete encoded token as a string using the secret
	signed, err := token.SignedString([]byte(o.jwtKey))
	if err != nil {
		http.Error(w, "failed to sign JWT", http.StatusInternalServerError)
		return
	}

	now := time.Now().Format(time.RFC3339)

	if err := u.AddToken(signed, now); err != nil {
		http.Error(w, "", http.StatusInternalServerError)
		return
	}

	u.Spec.Password = ""
	u.Spec.Tokens = map[string]string{signed: now}
	u.Spec.Role = nil

	body, err = json.Marshal(u.Spec)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Write(body)
}

func Login(w http.ResponseWriter, r *http.Request) {
	log.Debug("Login HTTP handler called")

	var user, pass string

	switch r.Method {
	case "GET":
		query := r.URL.Query()

		user = query.Get("user")
		if user == "" {
			http.Error(w, "no username provided", http.StatusBadRequest)
			return
		}

		pass = query.Get("pass")
		if pass == "" {
			http.Error(w, "no password provided", http.StatusBadRequest)
			return
		}

	case "POST":
		body, err := ioutil.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "no data provided in POST", http.StatusBadRequest)
			return
		}

		contract := struct {
			Username string `json:"user"`
			Password string `json:"pass"`
		}{}

		if err := json.Unmarshal(body, &contract); err != nil {
			http.Error(w, "invalid data provided in POST", http.StatusBadRequest)
			return
		}

		if user = contract.Username; user == "" {
			http.Error(w, "invalid username provided in POST", http.StatusBadRequest)
			return
		}

		if pass = contract.Password; pass == "" {
			http.Error(w, "invalid password provided in POST", http.StatusBadRequest)
			return
		}
	default:
		http.Error(w, "invalid method", http.StatusBadRequest)
		return
	}

	u, err := rbac.GetUser(user)
	if err != nil {
		http.Error(w, "cannot find user", http.StatusBadRequest)
		return
	}

	if err := u.ValidatePassword(pass); err != nil {
		http.Error(w, "invalid creds", http.StatusUnauthorized)
		return
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": u.Username(),
		"exp": time.Now().Add(24 * time.Hour).Unix(),
	})

	// Sign and get the complete encoded token as a string using the secret
	signed, err := token.SignedString([]byte(o.jwtKey))
	if err != nil {
		http.Error(w, "failed to sign JWT", http.StatusInternalServerError)
		return
	}

	now := time.Now().Format(time.RFC3339)

	if err := u.AddToken(signed, now); err != nil {
		http.Error(w, "", http.StatusInternalServerError)
		return
	}

	u.Spec.Password = ""
	u.Spec.Tokens = map[string]string{signed: now}

	body, err := json.Marshal(u.Spec)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Write(body)
}

func Logout(w http.ResponseWriter, r *http.Request) {
	log.Debug("Logout HTTP handler called")

	if o.jwtKey == "" {
		// Auth is disabled, so logging out is a no-op.
		w.WriteHeader(http.StatusNoContent)
		return
	}

	var (
		ctx   = r.Context()
		user  = ctx.Value("user").(string)
		token = ctx.Value("jwt").(string)
	)

	u, err := rbac.GetUser(user)
	if err != nil {
		http.Error(w, "cannot find user", http.StatusBadRequest)
		return
	}

	if err := u.DeleteToken(token); err != nil {
		http.Error(w, "", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func parseDuration(v string, d *time.Duration) error {
	var err error
	*d, err = time.ParseDuration(v)
	return err
}

func parseInt(v string, d *int) error {
	var err error
	*d, err = strconv.Atoi(v)
	return err
}
