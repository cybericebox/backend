package exerciseModel

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
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

// A seat run: the bot takes the human's seat, so both dossiers have the same links and paths; the bot's forbidden list adds
// every device with a console port it does not operate.
func TestHumanAndBotDossiersAgreeForASeatRun(t *testing.T) {
	v, _ := dossierVariant()
	v.Defender = &Defender{Seat: "pc1", OffLimits: []string{"web"}}
	creds := []DefenderCredential{{Device: "r1", Port: "console0", User: "admin", Value: "s3cret"}}
	human := HumanDossier(v, creds)
	run, tools, err := v.Topology.WithToolsDevices([]ToolsPlacement{{Image: "d:1", Seat: "pc1", Operates: []string{"r1", "core", "db"}}})
	if err != nil {
		t.Fatal(err)
	}
	bot := BotDossier(run, tools[0], []string{"r1", "core", "db"}, v.Defender, creds)
	if !reflect.DeepEqual(human.Links, bot.Links) || !reflect.DeepEqual(human.Forbidden, bot.Forbidden) || !reflect.DeepEqual(human.Credentials, bot.Credentials) {
		t.Fatalf("human %+v\nbot %+v", human, bot)
	}
	if !human.ShowsTopology || !bot.ShowsTopology {
		t.Fatal("full is the default")
	}
	partial := BotDossier(run, tools[0], []string{"r1"}, v.Defender, creds)
	if want := []string{"web", "core", "db"}; !reflect.DeepEqual(partial.Forbidden, want) {
		t.Fatalf("non-operated console devices are forbidden: %+v", partial.Forbidden)
	}
	none := BotDossier(run, tools[0], []string{"r1", "core", "db"}, nil, nil)
	if len(none.Forbidden) != 0 || none.Credentials == nil || !none.ShowsTopology {
		t.Fatalf("no block: %+v", none)
	}
}

// The files are root-only and follow the shapes of the design; topology.json comes only with topology: full and holds no
// image, no bootstrap and none of the platform's control.
func TestDossierFiles(t *testing.T) {
	v, _ := dossierVariant()
	v.Defender = &Defender{Seat: "pc1", OffLimits: []string{"web"}}
	run, tools, err := v.Topology.WithToolsDevices([]ToolsPlacement{{Image: "d:1", Seat: "pc1", Operates: []string{"r1", "core"}, Env: []EnvVar{{Name: "EDA_RUN_ID", Value: "run"}}}})
	if err != nil {
		t.Fatal(err)
	}
	creds := []DefenderCredential{{Device: "r1", Port: "console0", User: "admin", Value: "s3cret"}}
	out, err := run.WithDossierFiles(tools[0].ID, BotDossier(run, tools[0], []string{"r1", "core"}, v.Defender, creds))
	if err != nil {
		t.Fatal(err)
	}
	var files []BootstrapFile
	for _, d := range out.Devices {
		if d.ID == tools[0].ID {
			files = d.Bootstrap.Files
		}
	}
	if len(files) != 3 {
		t.Fatalf("three files: %+v", files)
	}
	byPath := map[string]BootstrapFile{}
	for _, f := range files {
		if f.Mode != "0600" || f.Owner != "root" || f.Group != "root" {
			t.Fatalf("root-only: %+v", f)
		}
		byPath[f.Path] = f
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
		"forbidden": []any{"web", "db"},
	}
	if !reflect.DeepEqual(consoles, wantConsoles) {
		t.Fatalf("consoles.json: %s", byPath[DossierConsolesPath].Content)
	}
	if got := byPath[DossierCredentialsPath].Content; got != `{"credentials":[{"device":"r1","port":"console0","user":"admin","value":"s3cret"}]}` {
		t.Fatalf("credentials.json: %s", got)
	}
	topology := byPath[DossierTopologyPath].Content
	for _, leak := range []string{"d:1", "EDA_RUN_ID", "ctl", "image", "bootstrap", "pc1"} {
		if strings.Contains(topology, leak) {
			t.Fatalf("topology.json holds %q: %s", leak, topology)
		}
	}
	if !strings.Contains(topology, `"name":"tools"`) || !strings.Contains(topology, `"console_ports":["console0","aux0"]`) {
		t.Fatalf("topology.json: %s", topology)
	}
	if run.Devices[len(run.Devices)-1].Bootstrap.Files != nil {
		t.Fatal("the input is not changed")
	}

	v.Defender.Topology = DefenderTopologyNone
	files, err = BotDossier(run, tools[0], []string{"r1"}, v.Defender, nil).Files(run)
	if err != nil || len(files) != 2 {
		t.Fatalf("no topology.json with topology none: %v %d", err, len(files))
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
