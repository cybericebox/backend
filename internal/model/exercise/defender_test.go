package exerciseModel

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/gofrs/uuid"
)

func defenderReason(t *testing.T, err error) (string, string) {
	t.Helper()
	var coded interface{ PublicContext() map[string]any }
	if !errors.As(err, &coded) || !errors.Is(err, ErrDefenderInvalid.Err()) {
		t.Fatalf("want ErrDefenderInvalid, got %v", err)
	}
	ctx := coded.PublicContext()
	return ctx["field"].(string), ctx["reason"].(string)
}

// dossierVariant is the seat lab (newSeatLab) as a variant: pc1 is the human's seat, cabled to r1/console0 (vtysh) and
// core/console0 (login); r1 also has aux0, db has console0 (native tcp).
func dossierVariant() (Variant, seatLab) {
	l := newSeatLab()
	for i := range l.topo.Devices {
		if l.topo.Devices[i].Name == "db" {
			l.topo.Devices[i].ConsolePorts = []ConsolePort{{Name: "console0", Native: &NativeSource{Kind: NativeTCP, Port: 2001}}}
		}
	}
	return Variant{Topology: l.topo}, l
}

func TestValidateDefender(t *testing.T) {
	v, _ := dossierVariant()
	if err := v.ValidateDefender(); err != nil {
		t.Fatalf("no block: %v", err)
	}
	v.Defender = &Defender{Seat: "pc1", Topology: DefenderTopologyNone, OffLimits: []string{"db"}, Credentials: []DefenderCredential{
		{Device: "r1", Port: "console0", User: "admin", Value: "s3cret", Secret: true}, {Device: "web", User: "root", Value: "toor"},
	}}
	if err := v.ValidateDefender(); err != nil {
		t.Fatalf("valid block: %v", err)
	}
	v.Defender.Topology = DefenderTopologyOperates
	v.Defender.Credentials[0].Value = strings.Repeat("c", 2*MaxDefenderCredentialSize) // sealed: not the author's text any more
	if err := v.ValidateDefender(); err != nil {
		t.Fatalf("operates is a value; a sealed secret has no length rule: %v", err)
	}
	for name, tc := range map[string]struct {
		edit          func(*Defender)
		field, reason string
	}{
		"seat unknown":                {func(d *Defender) { d.Seat = "nobody" }, "defender.seat", "seat_unknown"},
		"seat a switch":               {func(d *Defender) { d.Seat = "lan" }, "defender.seat", "seat_unknown"},
		"seat without serial":         {func(d *Defender) { d.Seat = "web" }, "defender.seat", "seat_no_serial"},
		"topology":                    {func(d *Defender) { d.Topology = "some" }, "defender.topology", "topology"},
		"off limits unknown":          {func(d *Defender) { d.OffLimits = []string{"db", "ghost"} }, "defender.off_limits[2]", "off_limits_unknown"},
		"off limits twice":            {func(d *Defender) { d.OffLimits = []string{"db", "db"} }, "defender.off_limits[2]", "off_limits_repeated"},
		"off limits cabled":           {func(d *Defender) { d.OffLimits = []string{"core"} }, "defender.off_limits[1]", "off_limits_cabled"},
		"credential device":           {func(d *Defender) { d.Credentials[1].Device = "ghost" }, "defender.credentials[2].device", "credential_unknown"},
		"credential port":             {func(d *Defender) { d.Credentials[0].Port = "console7" }, "defender.credentials[1].port", "credential_unknown"},
		"credential on a serial port": {func(d *Defender) { d.Credentials[0].Device, d.Credentials[0].Port = "pc1", "serial0" }, "defender.credentials[1].port", "credential_unknown"},
		"credential user":             {func(d *Defender) { d.Credentials[0].User = "a b" }, "defender.credentials[1].user", "credential_user"},
	} {
		t.Run(name, func(t *testing.T) {
			w, _ := dossierVariant()
			w.Defender = CopyDefender(v.Defender)
			tc.edit(w.Defender)
			field, reason := defenderReason(t, w.ValidateDefender())
			if field != tc.field || reason != tc.reason {
				t.Fatalf("got %s %s", field, reason)
			}
		})
	}
}

// The links of a device with serial ports say what runs at the other end: login (the default), program with its base name, or
// native.
func TestConsoleLinksOf(t *testing.T) {
	v, l := dossierVariant()
	v.Topology.Connections = append(v.Topology.Connections, Connection{Kind: ConnectionConsole, Endpoints: []Endpoint{port(l.pc1, "serial2"), port(l.db, "console0")}})
	pc1 := v.Topology.Devices[slices.IndexFunc(v.Topology.Devices, func(d Device) bool { return d.Name == "pc1" })]
	pc1.SerialPorts = append(pc1.SerialPorts, SerialPort{Name: "serial2"})
	got := v.Topology.ConsoleLinksOf(pc1)
	want := []ConsoleLink{
		{SerialPort: "serial0", Path: "/dev/ttyCON0", Device: "r1", ConsolePort: "console0", Kind: ConsoleKindProgram, Program: "vtysh"},
		{SerialPort: "serial1", Path: "/dev/ttyUSB1", Device: "core", ConsolePort: "console0", Kind: ConsoleKindLogin},
		{SerialPort: "serial2", Path: "/dev/ttyCON2", Device: "db", ConsolePort: "console0", Kind: ConsoleKindNative},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("links: %+v", got)
	}
}

// The forbidden list is one rule for the human and the bot: off_limits when named (an override), else the complement of what
// the defender operates. A seat run gives both the same dossier: the bot takes the human's seat and the opponent's operates.
func TestHumanAndBotDossiersAgreeForASeatRun(t *testing.T) {
	v, _ := dossierVariant()
	operates := []string{"r1", "core", "db"}
	v.Defender = &Defender{Seat: "pc1"}
	v.Opponents = []Opponent{{Role: OpponentDefense, Operates: operates, Seat: "pc1"}}
	creds := []DefenderCredential{{Device: "r1", Port: "console0", User: "admin", Value: "s3cret"}}
	human := HumanDossier(v, creds)
	run, tools, err := v.Topology.WithToolsDevices([]ToolsPlacement{{Image: "d:1", Seat: "pc1", Operates: operates}})
	if err != nil {
		t.Fatal(err)
	}
	bot := BotDossier(run, tools[0], operates, v.Defender, creds)
	if !reflect.DeepEqual(human, bot) {
		t.Fatalf("human %+v\nbot %+v", human, bot)
	}
	if len(human.Forbidden) != 0 || human.Topology != DefenderTopologyFull {
		t.Fatalf("everything with a console is operated, full is the default: %+v", human)
	}
	if got := BotDossier(run, tools[0], []string{"r1"}, v.Defender, creds).Forbidden; !reflect.DeepEqual(got, []string{"core", "db"}) {
		t.Fatalf("the complement of operates: %+v", got)
	}
	v.Defender.OffLimits = []string{"web"}
	if got := HumanDossier(v, nil).Forbidden; !reflect.DeepEqual(got, []string{"web"}) {
		t.Fatalf("off_limits overrides: %+v", got)
	}
	// Without an opponent at the seat, the human operates what the seat is cabled to.
	v.Opponents, v.Defender.OffLimits = nil, nil
	human = HumanDossier(v, nil)
	if !reflect.DeepEqual(human.Operates, []string{"r1", "core"}) || !reflect.DeepEqual(human.Forbidden, []string{"db"}) {
		t.Fatalf("the seat's cables: %+v", human)
	}
}

// The files are root-only and follow the shapes of the design; topology.json is written for full and operates, never names
// another tools device or the platform's control, and holds no image and no bootstrap.
func TestDossierFiles(t *testing.T) {
	v, _ := dossierVariant()
	v.Defender = &Defender{Seat: "pc1", OffLimits: []string{"web"}}
	run, tools, err := v.Topology.WithToolsDevices([]ToolsPlacement{
		{Image: "d:1", Seat: "pc1", Operates: []string{"r1", "core"}, Env: []EnvVar{{Name: "EDA_RUN_ID", Value: "run"}}},
		{Image: "a:1", Foothold: "lan"},
	})
	if err != nil {
		t.Fatal(err)
	}
	platform := map[uuid.UUID]bool{tools[0].ID: true, tools[1].ID: true}
	creds := []DefenderCredential{{Device: "r1", Port: "console0", User: "admin", Value: "s3cret"}}
	files := func(d Dossier) map[string]BootstrapFile {
		t.Helper()
		out, err := run.WithDossierFiles(tools[0], d, platform)
		if err != nil {
			t.Fatal(err)
		}
		byPath := map[string]BootstrapFile{}
		for _, dev := range out.Devices {
			if dev.ID == tools[0].ID {
				for _, f := range dev.Bootstrap.Files {
					if f.Mode != "0600" || f.Owner != "root" || f.Group != "root" {
						t.Fatalf("root-only: %+v", f)
					}
					byPath[f.Path] = f
				}
			}
		}
		return byPath
	}
	byPath := files(BotDossier(run, tools[0], []string{"r1", "core"}, v.Defender, creds))
	if len(byPath) != 3 {
		t.Fatalf("three files: %+v", byPath)
	}
	var consoles map[string]any
	if err = json.Unmarshal([]byte(byPath[DossierConsolesPath].Content), &consoles); err != nil {
		t.Fatal(err)
	}
	wantConsoles := map[string]any{
		"links": []any{
			map[string]any{"serial_port": "serial0", "path": "/dev/ttyCON0", "device": "r1", "console_kind": "program", "program": "vtysh"},
			map[string]any{"serial_port": "serial1", "path": "/dev/ttyUSB1", "device": "core", "console_kind": "login", "program": ""},
		},
		"forbidden": []any{"web"},
	}
	if !reflect.DeepEqual(consoles, wantConsoles) {
		t.Fatalf("consoles.json: %s", byPath[DossierConsolesPath].Content)
	}
	if got := byPath[DossierCredentialsPath].Content; got != `{"credentials":[{"device":"r1","port":"console0","user":"admin","value":"s3cret"}]}` {
		t.Fatalf("credentials.json: %s", got)
	}
	topology := byPath[DossierTopologyPath].Content
	for _, leak := range []string{"d:1", "a:1", "EDA_RUN_ID", "ctl", "image", "bootstrap", "pc1", "tools-2"} {
		if strings.Contains(topology, leak) {
			t.Fatalf("topology.json holds %q: %s", leak, topology)
		}
	}
	if !strings.Contains(topology, `"name":"tools"`) || !strings.Contains(topology, `"console_ports":["console0","aux0"]`) || !strings.Contains(topology, `"name":"web"`) {
		t.Fatalf("topology.json: %s", topology)
	}

	v.Defender.Topology = DefenderTopologyOperates
	subset := files(BotDossier(run, tools[0], []string{"r1", "core"}, v.Defender, nil))[DossierTopologyPath].Content
	var shape struct {
		Devices []struct {
			Name string `json:"name"`
		} `json:"devices"`
	}
	if err = json.Unmarshal([]byte(subset), &shape); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range shape.Devices {
		names = append(names, d.Name)
	}
	if !reflect.DeepEqual(names, []string{"r1", "core", "tools"}) || !strings.Contains(subset, `{"device":"back","interface":"GigabitEthernet0/2"}`) || strings.Contains(subset, "tools-2") {
		t.Fatalf("operates: its own devices and their links: %s", subset)
	}

	v.Defender.Topology = DefenderTopologyNone
	got, err := BotDossier(run, tools[0], []string{"r1"}, v.Defender, nil).Files(run, tools[0], platform)
	if err != nil || len(got) != 2 {
		t.Fatalf("no topology.json with topology none: %v %d", err, len(got))
	}
}

func TestMissingSecretsCountTheDefenderCredentials(t *testing.T) {
	v, _ := dossierVariant()
	v.Defender = &Defender{Credentials: []DefenderCredential{{Device: "r1", Value: "x", Secret: true}, {Device: "core", Secret: true}, {Device: "db"}}}
	got := MissingSecrets([]Variant{v})
	if len(got) != 1 || got[0].Device != "core" || got[0].Key != "defender.credentials[2]" || got[0].Variant != 1 {
		t.Fatalf("missing: %+v", got)
	}
}
