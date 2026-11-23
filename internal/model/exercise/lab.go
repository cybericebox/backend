package exerciseModel

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

// What the laboratory receives of a device and of a state task (agent API contract 1.5):
//
//   - the resolved bootstrap list of a device with files or a script (Device.HasBootstrapList, ResolvedBootstrap), next to
//     the existing environment path that still carries bootstrap.env;
//   - the service command of a device (Device.LabService): the author's command and args, else the image configuration;
//   - the access list of a device (Device.Access: http, terminal, and the reserved vnc and rdp);
//   - the checks of a lab: the conditions of a state task grouped per (task, device) (BuildLabChecks).
//
// The translation to the wire messages is in delivery/infrastructure/labagent.

// CheckIDSeparator separates the task key from the position of the device group in the id of a laboratory check.
// A task key never holds it, so the id maps back to its task unambiguously.
const CheckIDSeparator = ":"

// DefaultCheckTimeLimit is the time limit of a check whose conditions give none (the laboratory's own default).
const DefaultCheckTimeLimit = 30 * time.Second

// ImageService is the start configuration an image carries: what it runs and as whom.
type ImageService struct {
	Entrypoint []string `json:"entrypoint,omitempty"`
	Cmd        []string `json:"cmd,omitempty"`
	User       string   `json:"user,omitempty"`
	WorkingDir string   `json:"working_dir,omitempty"`
}

// ParseImageService reads the start configuration out of an OCI/Docker image config blob (the "config" object: Entrypoint,
// Cmd, User, WorkingDir). A config without them gives the zero value; a body that is not JSON is an error.
func ParseImageService(body []byte) (ImageService, error) {
	var cfg struct {
		Config struct {
			Entrypoint []string `json:"Entrypoint"`
			Cmd        []string `json:"Cmd"`
			User       string   `json:"User"`
			WorkingDir string   `json:"WorkingDir"`
		} `json:"config"`
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		return ImageService{}, err
	}
	return ImageService{
		Entrypoint: cfg.Config.Entrypoint, Cmd: cfg.Config.Cmd, User: cfg.Config.User, WorkingDir: cfg.Config.WorkingDir,
	}, nil
}

// ServiceUserRoot is the user a service runs as when its image declares none: the account with id 0, written as a number
// because the launcher resolves a number without /etc/passwd (an image built from scratch has none) while a name must be
// in it.
const ServiceUserRoot = "0"

// ServiceUser is the user the service of the image runs as, in the form the image gives it ("app", "1000", "app:web",
// "1000:1000": the launcher takes all of them); the root account when the image declares none.
func (s ImageService) ServiceUser() string {
	if s.User == "" {
		return ServiceUserRoot
	}
	return s.User
}

// IsZero reports an image configuration that says nothing.
func (s ImageService) IsZero() bool {
	return len(s.Entrypoint) == 0 && len(s.Cmd) == 0 && s.User == "" && s.WorkingDir == ""
}

// LabService is what the laboratory is told to run after the bootstrap of a device.
type LabService struct {
	Command    []string
	Args       []string
	User       string
	WorkingDir string
}

// HasBootstrapList reports whether the laboratory gets a bootstrap list for the device: files or a script. A device whose
// bootstrap only has env needs none (the environment travels on its own path and the service starts as the image says).
func (d Device) HasBootstrapList() bool {
	return len(d.Bootstrap.Files) > 0 || d.Bootstrap.Script != ""
}

// NeedsServiceCommand reports whether the laboratory must be told what to run: the device has a bootstrap list (the
// launcher replaces itself with the service), console or serial ports (the launcher starts the port keeper before the
// service, and the laboratory refuses such a device without a service command and its user) or the author set a command or
// arguments.
func (d Device) NeedsServiceCommand() bool {
	return d.IsNode() && (d.HasBootstrapList() || d.HasPorts() || len(d.Command) > 0 || len(d.Args) > 0)
}

// LabService resolves the service command of the device: the author's command and args when set, else the values of the
// image configuration. Docker's rules apply: an author command replaces the entrypoint and drops the image's cmd; args
// alone replace the image's cmd. The user and the working directory are the image's (a user is always sent: the root
// account when the image declares none). When the entrypoint is empty the
// command is the argument list (the image runs its cmd as the whole command). nil when the laboratory need not be told
// (the image decides). An error names the device when a command is needed but nothing gives one, or when the image's
// configuration is not known (ErrServiceUserUnknown).
func (d Device) LabService() (*LabService, error) {
	if !d.NeedsServiceCommand() {
		return nil, nil
	}
	var image ImageService
	if d.ImageService != nil {
		image = *d.ImageService
	}
	command, args := image.Entrypoint, image.Cmd
	switch {
	case len(d.Command) > 0:
		command, args = d.Command, d.Args
	case len(d.Args) > 0:
		args = d.Args
	}
	if len(command) == 0 {
		command, args = args, nil
	}
	if len(command) == 0 {
		return nil, ErrServiceCommandUnknown.WithPublicContext("device", d.Name).Err()
	}
	user, known := d.ResolveServiceUser(d.ImageService)
	if !known {
		return nil, ErrServiceUserUnknown.WithPublicContext("device", d.Name).WithPublicContext("field", "image").Err()
	}
	return &LabService{
		Command:    append([]string(nil), command...),
		Args:       append([]string(nil), args...),
		User:       user,
		WorkingDir: image.WorkingDir,
	}, nil
}

// ServiceUserOf is the one rule for the user a service runs as: the author's value when there is one, else the user the
// image declares (the root account when it declares none), else unknown: with no image configuration the platform does not
// guess, because an empty user would start the service as root even for an image that declares a non-root USER.
func ServiceUserOf(author string, image *ImageService) (user string, known bool) {
	if author != "" {
		return author, true
	}
	if image == nil {
		return "", false
	}
	return image.ServiceUser(), true
}

// authoredServiceUser is the service user the author wrote for the device. The schema has no such key yet: when it does,
// this is the only place that changes (ServiceUserOf already lets it win over the image).
func (d Device) authoredServiceUser() string { return "" }

// ResolveServiceUser is ServiceUserOf for this device with the configuration of its image (nil: not known). The deploy and
// the publish gate both ask it.
func (d Device) ResolveServiceUser(image *ImageService) (user string, known bool) {
	return ServiceUserOf(d.authoredServiceUser(), image)
}

// LabCheckPart is one observation of a check with the substitutions made.
type LabCheckPart struct {
	// Condition holds the observation (exactly one of its kinds is set) with the substitutions made.
	Condition Condition
}

// LabCheck is one check the laboratory runs: every condition of one state task on one device. The task is solved when all
// its checks pass.
type LabCheck struct {
	// ID is "<task key>:<n>" with n the 1-based position of the device among the devices of the task's conditions.
	ID      string
	TaskKey string
	// Device is the device's name inside the lab.
	Device string
	// Mode is the mode of the task (once is the default).
	Mode             StateMode
	ScheduleSeconds  int32
	TimeLimitSeconds int32
	Manual           bool
	Parts            []LabCheckPart
}

// CheckID is the id of the n-th (1-based) check of a task.
func CheckID(taskKey string, n int) string { return taskKey + CheckIDSeparator + strconv.Itoa(n) }

// TaskKeyOfCheck maps the id of a laboratory check back to the key of its task; false for an id this platform did not
// make.
func TaskKeyOfCheck(id string) (string, bool) {
	key, position, found := strings.Cut(id, CheckIDSeparator)
	if !found || !ValidKey(key) {
		return "", false
	}
	if n, err := strconv.Atoi(position); err != nil || n < 1 {
		return "", false
	}
	return key, true
}

// ceilSeconds rounds a duration up to whole seconds.
func ceilSeconds(d time.Duration) int32 {
	seconds := (d + time.Second - 1) / time.Second
	return int32(min(seconds, math.MaxInt32))
}

// BuildLabChecks turns the resolved conditions of the state tasks into the checks of the lab, grouped per (task, device) in
// the order the devices first appear in the task. topology names the devices (the lab name of each). The schedule is the
// task's interval; the time limit is the longest condition timeout of the group, else DefaultCheckTimeLimit, capped by
// the interval when the interval is shorter (a schedule never runs shorter than the time limit: when an explicit timeout
// is longer than the interval, the schedule is raised to it).
func (v Variant) BuildLabChecks(topology Topology, resolved []ResolvedConditions) ([]LabCheck, error) {
	names := LabDeviceNames(topology.Devices)
	nameByID := make(map[string]string, len(topology.Devices))
	for i, d := range topology.Devices {
		nameByID[d.ID.String()] = names[i]
	}
	states := make(map[string]*StateCheck, len(v.Tasks))
	for i := range v.Tasks {
		if v.Tasks[i].Check.State != nil {
			states[v.Tasks[i].Key] = v.Tasks[i].Check.State
		}
	}
	var out []LabCheck
	for _, task := range resolved {
		state, ok := states[task.TaskKey]
		if !ok {
			return nil, ErrStateInvalid.WithPublicContext("task", task.TaskKey).WithPublicContext("field", "check.state").Err()
		}
		interval, err := parseInterval(state.Interval)
		if err != nil {
			return nil, ErrStateInvalid.WithPublicContext("task", task.TaskKey).WithPublicContext("field", "check.state.interval").Err()
		}
		mode := state.Mode
		if mode == "" {
			mode = StateModeOnce
		}
		var order []string
		groups := map[string]*LabCheck{}
		limits := map[string]time.Duration{}
		for i, c := range task.Conditions {
			name, found := nameByID[c.DeviceID.String()]
			if !found {
				return nil, ErrStateInvalid.WithPublicContext("task", task.TaskKey).
					WithPublicContext("field", fmt.Sprintf("check.state.conditions[%d].device", i+1)).
					Err()
			}
			group, seen := groups[name]
			if !seen {
				group = &LabCheck{
					ID:      CheckID(task.TaskKey, len(order)+1),
					TaskKey: task.TaskKey,
					Device:  name,
					Mode:    mode,
					Manual:  state.Manual,
				}
				groups[name] = group
				order = append(order, name)
			}
			group.Parts = append(group.Parts, LabCheckPart{Condition: c})
			if c.Timeout != "" {
				if timeout, parseErr := time.ParseDuration(c.Timeout); parseErr == nil && timeout > limits[name] {
					limits[name] = timeout
				}
			}
		}
		for _, name := range order {
			check := groups[name]
			limit, explicit := limits[name], limits[name] > 0
			if !explicit {
				limit = DefaultCheckTimeLimit
				if interval > 0 && interval < limit {
					limit = interval
				}
			}
			schedule := interval
			if schedule > 0 && schedule < limit {
				schedule = limit
			}
			check.TimeLimitSeconds = ceilSeconds(limit)
			check.ScheduleSeconds = ceilSeconds(schedule)
			out = append(out, *check)
		}
	}
	return out, nil
}

// parseInterval reads the interval of a state check; empty or "0" is no schedule.
func parseInterval(s string) (time.Duration, error) {
	if s == "" || s == "0" {
		return 0, nil
	}
	return time.ParseDuration(s)
}

// LabNeeds is what a variant asks of the laboratory beyond a plain lab: the options the target laboratory must have and the
// sizes it must accept. It is computed from the resolved instance, so the sizes are the real ones.
type LabNeeds struct {
	// Checks: some state task has checks; Terminal: some device has a terminal; Bootstrap: some device has a bootstrap list
	// (files or script) or a service command to hand over.
	Checks    bool
	Terminal  bool
	Bootstrap bool
	// ChecksPerLab and MaxParts are the most checks of the lab and the most parts of one check.
	ChecksPerLab int
	MaxParts     int
	// Observations are the observation kinds the checks use, sorted and without repeats; the file keys type and checksum
	// are named "file.type" and "file.checksum". The laboratory must offer every one.
	Observations []string
	// MaxCheckTimeLimit and MaxCheckSchedule are the largest time limit and schedule, in seconds.
	MaxCheckTimeLimit int
	// LongestCheck is the id of the check with the largest time limit, to name its task when the laboratory's ceiling is passed.
	LongestCheck     string
	MaxCheckSchedule int
	// Bootstrap sizes: the most files of a device, the largest script and file, the most bytes of one device (script and files),
	// the longest time limit in seconds.
	MaxFiles          int
	MaxScriptBytes    int
	MaxFileBytes      int
	MaxBootstrapBytes int
	MaxBootstrapLimit int
	// Gateways is the number of gateways (VPN and Internet) of the lab.
	Gateways int
	// VPN and Internet: the lab has a VPN gateway / an Internet gateway (a laboratory that runs none refuses it).
	VPN, Internet bool
	// HTTP, RDP and VNC: some device has that access (the terminal is Terminal above); the laboratory must serve each kind.
	HTTP, RDP, VNC bool
	// Registries are the registries the device images come from, canonical, sorted, without repeats; images of the platform itself
	// ("platform:..." and the node's own forwarder) are not in it. The laboratory must allow each one.
	Registries []string
	// ConsoleLines: some device has a console or serial port, or the lab has a console cable; ConsoleCables counts the cables.
	// The laboratory must serve console lines (FeaturesResponse.console_cables, contract 3.1).
	ConsoleLines  bool
	ConsoleCables int
	// KernelLog: some device puts the guest kernel's log on a console port. The laboratory must offer it (contract 3.2,
	// FeaturesResponse.console_cables.kernel_log).
	KernelLog bool
}

// RegistryOf is the canonical registry an image reference is pulled from: docker.io for a reference that names none (and for
// the Docker Hub spellings), the host with its port otherwise, lower-case. ok is false for what is not pulled from a
// registry the laboratory checks: an empty reference, a platform reference and the node's own forwarder.
func RegistryOf(image string) (registry string, ok bool) {
	image = strings.TrimSpace(image)
	if image == "" || strings.HasPrefix(image, "platform:") {
		return "", false
	}
	host, _, hasPath := strings.Cut(image, "/")
	if !hasPath || !(strings.ContainsAny(host, ".:") || host == "localhost") {
		return "docker.io", true
	}
	host = strings.ToLower(host)
	switch host {
	case "index.docker.io", "registry-1.docker.io", "registry.hub.docker.com":
		return "docker.io", true
	case "localhost:5035":
		return "", false
	}
	return host, true
}

// NeedsOf measures the topology (with its resolved bootstraps) and the checks.
func NeedsOf(t Topology, checks []LabCheck) LabNeeds {
	var n LabNeeds
	for _, d := range t.Devices {
		if d.Type.IsGateway() {
			n.Gateways++
		}
		n.VPN = n.VPN || d.Type == DeviceTypeVPNGateway
		n.Internet = n.Internet || d.Type == DeviceTypeInternetGateway
		if d.IsNode() {
			n.Terminal = n.Terminal || d.HasAccess(AccessTerminal)
			n.HTTP = n.HTTP || d.HasAccess(AccessHTTP)
			n.RDP = n.RDP || d.HasAccess(AccessRDP)
			n.VNC = n.VNC || d.HasAccess(AccessVNC)
			if registry, ok := RegistryOf(d.Image); ok && !slices.Contains(n.Registries, registry) {
				n.Registries = append(n.Registries, registry)
			}
		}
		if d.NeedsServiceCommand() {
			n.Bootstrap = true
		}
		if !d.HasBootstrapList() {
			continue
		}
		n.MaxFiles = max(n.MaxFiles, len(d.Bootstrap.Files))
		n.MaxScriptBytes = max(n.MaxScriptBytes, len(d.Bootstrap.Script))
		total := len(d.Bootstrap.Script)
		for _, f := range d.Bootstrap.Files {
			n.MaxFileBytes = max(n.MaxFileBytes, len(f.Content))
			total += len(f.Content)
		}
		n.MaxBootstrapBytes = max(n.MaxBootstrapBytes, total)
		if d.Bootstrap.Timeout != "" {
			if timeout, err := time.ParseDuration(d.Bootstrap.Timeout); err == nil {
				n.MaxBootstrapLimit = max(n.MaxBootstrapLimit, int(ceilSeconds(timeout)))
			}
		}
	}
	slices.Sort(n.Registries)
	n.ConsoleLines, n.ConsoleCables = t.UsesConsoleLines(), t.ConsoleCables()
	n.KernelLog = slices.ContainsFunc(t.Devices, func(d Device) bool { return d.KernelLogPort() != "" })
	n.Checks = len(checks) > 0
	n.ChecksPerLab = len(checks)
	kinds := map[string]struct{}{}
	for _, c := range checks {
		for _, part := range c.Parts {
			for _, kind := range part.Condition.Kinds() {
				kinds[string(kind)] = struct{}{}
			}
			if f := part.Condition.File; f != nil {
				if f.Type != "" {
					kinds["file.type"] = struct{}{}
				}
				if f.Checksum != "" {
					kinds["file.checksum"] = struct{}{}
				}
			}
		}
	}
	n.Observations = slices.Sorted(maps.Keys(kinds))
	for _, c := range checks {
		n.MaxParts = max(n.MaxParts, len(c.Parts))
		if int(c.TimeLimitSeconds) > n.MaxCheckTimeLimit {
			n.MaxCheckTimeLimit, n.LongestCheck = int(c.TimeLimitSeconds), c.ID
		}
		n.MaxCheckSchedule = max(n.MaxCheckSchedule, int(c.ScheduleSeconds))
	}
	return n
}
