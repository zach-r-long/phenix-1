package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"phenix/internal/common"
	"phenix/internal/mm"
	"phenix/tmpl"
	"phenix/types"
	ifaces "phenix/types/interfaces"
)

type Startup struct{}

func (Startup) Init(...Option) error {
	return nil
}

func (Startup) Name() string {
	return "startup"
}

func (this *Startup) Configure(ctx context.Context, exp *types.Experiment) error {
	return nil
}

func (this Startup) PreStart(ctx context.Context, exp *types.Experiment) error {
	var (
		startupDir = exp.Spec.BaseDir() + "/startup"
		imageDir   = common.PhenixBase + "/images/"
	)

	if err := os.MkdirAll(startupDir, 0755); err != nil {
		return fmt.Errorf("creating experiment startup directory path: %w", err)
	}

	for _, node := range exp.Spec.Topology().Nodes() {
		// Check if user provided an absolute path to image. If not, prepend path
		// with default image path.
		imagePath := node.Hardware().Drives()[0].Image()

		if !filepath.IsAbs(imagePath) {
			imagePath = imageDir + imagePath
		}

		// check if the disk image is present, if not set do not boot to true
		if _, err := os.Stat(imagePath); os.IsNotExist(err) {
			node.General().SetDoNotBoot(true)
		}

		// if type is router, skip it and continue
		if node.Type() == "Router" {
			continue
		}

		switch node.Hardware().OSType() {
		case "linux", "rhel", "centos":
			var (
				hostnameFile = startupDir + "/" + node.General().Hostname() + "-hostname.sh"
				timezoneFile = startupDir + "/" + node.General().Hostname() + "-timezone.sh"
				ifaceFile    = startupDir + "/" + node.General().Hostname() + "-interfaces.sh"
			)

			node.AddInject(
				hostnameFile,
				"/etc/phenix/startup/1_hostname-start.sh",
				"0755", "",
			)

			node.AddInject(
				timezoneFile,
				"/etc/phenix/startup/2_timezone-start.sh",
				"0755", "",
			)

			node.AddInject(
				ifaceFile,
				"/etc/phenix/startup/3_interfaces-start.sh",
				"0755", "",
			)

			timeZone := "Etc/UTC"

			if err := tmpl.CreateFileFromTemplate("linux_hostname.tmpl", node.General().Hostname(), hostnameFile); err != nil {
				return fmt.Errorf("generating linux hostname script: %w", err)
			}

			if err := tmpl.CreateFileFromTemplate("linux_timezone.tmpl", timeZone, timezoneFile); err != nil {
				return fmt.Errorf("generating linux timezone script: %w", err)
			}

			if err := tmpl.CreateFileFromTemplate("linux_interfaces.tmpl", node, ifaceFile); err != nil {
				return fmt.Errorf("generating linux interfaces script: %w", err)
			}
		case "windows":
			startupFile := startupDir + "/" + node.General().Hostname() + "-startup.ps1"

			node.AddInject(
				startupFile,
				"/phenix/startup/20-startup.ps1",
				"0755", "",
			)

			if incl, ok := node.GetAnnotation("includes-phenix-startup"); !ok || incl == "false" || incl == false {
				node.AddInject(
					startupDir+"/phenix-startup.ps1",
					"/phenix/phenix-startup.ps1",
					"0755", "",
				)

				if err := tmpl.RestoreAsset(startupDir, "phenix-startup.ps1"); err != nil {
					return fmt.Errorf("restoring phenix startup script: %w", err)
				}

				node.AddInject(
					startupDir+"/startup-scheduler.cmd",
					"ProgramData/Microsoft/Windows/Start Menu/Programs/Startup/startup_scheduler.cmd",
					"0755", "",
				)

				if err := tmpl.RestoreAsset(startupDir, "startup-scheduler.cmd"); err != nil {
					return fmt.Errorf("restoring windows startup scheduler: %w", err)
				}
			}

			// Temporary struct to send to the Windows Startup template.
			data := struct {
				Node     ifaces.NodeSpec
				Metadata map[string]interface{}
			}{
				Node:     node,
				Metadata: make(map[string]interface{}),
			}

			// Check to see if a scenario exists for this experiment and if it
			// contains a "startup" app. If so, see if this node has a metadata entry
			// in the scenario app configuration.
			for _, app := range exp.Apps() {
				if app.Name() == "startup" {
					for _, host := range app.Hosts() {
						if host.Hostname() == node.General().Hostname() {
							data.Metadata = host.Metadata()
						}
					}
				}
			}

			if err := tmpl.CreateFileFromTemplate("windows_startup.tmpl", data, startupFile); err != nil {
				return fmt.Errorf("generating windows startup script: %w", err)
			}
		}
	}

	return nil
}

func (Startup) PostStart(ctx context.Context, exp *types.Experiment) error {
	for _, node := range exp.Spec.Topology().Nodes() {
		if strings.EqualFold(node.Hardware().OSType(), "windows") {
			// Windows 10 doesn't automatically run scripts in the startup folder
			if ver, ok := node.GetAnnotation("windows-version"); ok && (ver == "10" || ver == 10) {
				_, err := mm.ExecC2Command(
					mm.C2NS(exp.Metadata.Name),
					mm.C2VM(node.General().Hostname()),
					mm.C2SkipActiveClientCheck(true),
					mm.C2Command(`powershell.exe -noprofile -executionpolicy bypass -file /phenix/phenix-startup.ps1`),
				)

				if err != nil {
					return fmt.Errorf("execute C2 command to run Windows startup script: %w", err)
				}
			}
		}
	}

	return nil
}

func (Startup) Running(ctx context.Context, exp *types.Experiment) error {
	return nil
}

func (Startup) Cleanup(ctx context.Context, exp *types.Experiment) error {
	return nil
}
