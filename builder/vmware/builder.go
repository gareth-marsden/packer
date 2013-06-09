package vmware

import (
	"errors"
	"fmt"
	"github.com/mitchellh/mapstructure"
	"github.com/mitchellh/multistep"
	"github.com/mitchellh/packer/packer"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"time"
)

const BuilderId = "mitchellh.vmware"

type Builder struct {
	config config
	driver Driver
	runner multistep.Runner
}

type config struct {
	DiskName        string            `mapstructure:"vmdk_name"`
	GuestOSType     string            `mapstructure:"guest_os_type"`
	ISOUrl          string            `mapstructure:"iso_url"`
	VMName          string            `mapstructure:"vm_name"`
	OutputDir       string            `mapstructure:"output_directory"`
	HTTPDir         string            `mapstructure:"http_directory"`
	HTTPPortMin     uint              `mapstructure:"http_port_min"`
	HTTPPortMax     uint              `mapstructure:"http_port_max"`
	BootCommand     []string          `mapstructure:"boot_command"`
	BootWait        time.Duration     ``
	ShutdownCommand string            `mapstructure:"shutdown_command"`
	ShutdownTimeout time.Duration     ``
	SSHUser         string            `mapstructure:"ssh_username"`
	SSHPassword     string            `mapstructure:"ssh_password"`
	SSHWaitTimeout  time.Duration     ``
	VMXData         map[string]string `mapstructure:"vmx_data"`
	VNCPortMin      uint              `mapstructure:"vnc_port_min"`
	VNCPortMax      uint              `mapstructure:"vnc_port_max"`

	RawBootWait        string `mapstructure:"boot_wait"`
	RawShutdownTimeout string `mapstructure:"shutdown_timeout"`
	RawSSHWaitTimeout  string `mapstructure:"ssh_wait_timeout"`
}

func (b *Builder) Prepare(raw interface{}) (err error) {
	err = mapstructure.Decode(raw, &b.config)
	if err != nil {
		return
	}

	if b.config.DiskName == "" {
		b.config.DiskName = "disk"
	}

	if b.config.GuestOSType == "" {
		b.config.GuestOSType = "other"
	}

	if b.config.VMName == "" {
		b.config.VMName = "packer"
	}

	if b.config.HTTPPortMin == 0 {
		b.config.HTTPPortMin = 8000
	}

	if b.config.HTTPPortMax == 0 {
		b.config.HTTPPortMax = 9000
	}

	if b.config.VNCPortMin == 0 {
		b.config.VNCPortMin = 5900
	}

	if b.config.VNCPortMax == 0 {
		b.config.VNCPortMax = 6000
	}

	if b.config.OutputDir == "" {
		b.config.OutputDir = "vmware"
	}

	// Accumulate any errors
	errs := make([]error, 0)

	if b.config.HTTPPortMin > b.config.HTTPPortMax {
		errs = append(errs, errors.New("http_port_min must be less than http_port_max"))
	}

	if b.config.ISOUrl == "" {
		errs = append(errs, errors.New("An iso_url must be specified."))
	}

	if b.config.SSHUser == "" {
		errs = append(errs, errors.New("An ssh_username must be specified."))
	}

	if b.config.RawBootWait != "" {
		b.config.BootWait, err = time.ParseDuration(b.config.RawBootWait)
		if err != nil {
			errs = append(errs, fmt.Errorf("Failed parsing boot_wait: %s", err))
		}
	}

	if b.config.RawShutdownTimeout == "" {
		b.config.RawShutdownTimeout = "5m"
	}

	b.config.ShutdownTimeout, err = time.ParseDuration(b.config.RawShutdownTimeout)
	if err != nil {
		errs = append(errs, fmt.Errorf("Failed parsing shutdown_timeout: %s", err))
	}

	if b.config.RawSSHWaitTimeout == "" {
		b.config.RawSSHWaitTimeout = "20m"
	}

	b.config.SSHWaitTimeout, err = time.ParseDuration(b.config.RawSSHWaitTimeout)
	if err != nil {
		errs = append(errs, fmt.Errorf("Failed parsing ssh_wait_timeout: %s", err))
	}

	if b.config.VNCPortMin > b.config.VNCPortMax {
		errs = append(errs, fmt.Errorf("vnc_port_min must be less than vnc_port_max"))
	}

	b.driver, err = b.newDriver()
	if err != nil {
		errs = append(errs, fmt.Errorf("Failed creating VMware driver: %s", err))
	}

	if len(errs) > 0 {
		return &packer.MultiError{errs}
	}

	return nil
}

func (b *Builder) Run(ui packer.Ui, hook packer.Hook) packer.Artifact {
	// Seed the random number generator
	rand.Seed(time.Now().UTC().UnixNano())

	steps := []multistep.Step{
		&stepPrepareOutputDir{},
		&stepCreateDisk{},
		&stepCreateVMX{},
		&stepHTTPServer{},
		&stepConfigureVNC{},
		&stepRun{},
		&stepTypeBootCommand{},
		&stepWaitForSSH{},
		&stepProvision{},
		&stepShutdown{},
	}

	// Setup the state bag
	state := make(map[string]interface{})
	state["config"] = &b.config
	state["driver"] = b.driver
	state["hook"] = hook
	state["ui"] = ui

	// Run!
	b.runner = &multistep.BasicRunner{Steps: steps}
	b.runner.Run(state)

	// If we were interrupted or cancelled, then just exit.
	if _, ok := state[multistep.StateCancelled]; ok {
		return nil
	}

	if _, ok := state[multistep.StateHalted]; ok {
		return nil
	}

	// Compile the artifact list
	files := make([]string, 0, 10)
	visit := func(path string, info os.FileInfo, err error) error {
		files = append(files, path)
		return err
	}

	if err := filepath.Walk(b.config.OutputDir, visit); err != nil {
		ui.Error(fmt.Sprintf("Error collecting result files: %s", err))
		return nil
	}

	return &Artifact{b.config.OutputDir, files}
}

func (b *Builder) Cancel() {
	if b.runner != nil {
		log.Println("Cancelling the step runner...")
		b.runner.Cancel()
	}
}

func (b *Builder) newDriver() (Driver, error) {
	fusionAppPath := "/Applications/VMware Fusion.app"
	driver := &Fusion5Driver{fusionAppPath}
	if err := driver.Verify(); err != nil {
		return nil, err
	}

	return driver, nil
}