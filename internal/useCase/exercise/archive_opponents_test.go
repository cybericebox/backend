package exercise

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
)

// opponentDoc is a one-variant document with two tasks (recon, root) and two devices (web, db) and the given opponents.
func opponentDoc(opponents string) string {
	block := ""
	if opponents != "" {
		block = `,"opponents":` + opponents
	}
	return v2Header + `"exercise":{"name":"Sample"},"versions":[{"status":"draft","variants":[{` +
		`"tasks":[{"key":"recon","name":"Recon","check":{"answer":{"type":"flag","values":["ICE{a}"]}}},{"key":"root","name":"Root","check":{"answer":{"type":"flag","values":["ICE{b}"]}}}],` +
		`"topology":{"devices":[{"name":"web","type":"container"},{"name":"db","type":"container"},{"name":"sw","type":"unmanaged-switch"}]}` + block + `}]}]}`
}

func parseOpponents(t *testing.T, opponents string) ([]exerciseModel.Opponent, error) {
	t.Helper()
	doc, err := decodeArchiveV2([]byte(opponentDoc(opponents)))
	if err != nil {
		return nil, err
	}
	parsed, err := doc.toParsed(nil)
	if err != nil {
		return nil, err
	}
	return parsed.Versions[0].Variants[0].Opponents, nil
}

const validOpponents = `[
 {"role":"attack","foothold":"sw","band":3,"goal":["recon","root"],"start":"immediate","brief":"Be quiet."},
 {"role":"defense","band":2,"goal":["root"],"start":{"after":"recon"},"operates":["web","db"]},
 {"role":"attack-defense","foothold":"sw","band":6,"goal":["recon"],"operates":["web"]}
]`

func TestOpponentsAreReadFromTheDocumentWithAllTheirFields(t *testing.T) {
	got, err := parseOpponents(t, validOpponents)
	if err != nil {
		t.Fatal(err)
	}
	want := []exerciseModel.Opponent{
		{Role: exerciseModel.OpponentAttack, Foothold: "sw", Band: 3, Goal: []string{"recon", "root"}, Brief: "Be quiet."},
		{Role: exerciseModel.OpponentDefense, Band: 2, Goal: []string{"root"}, Start: exerciseModel.OpponentStart{After: "recon"}, Operates: []string{"web", "db"}},
		{Role: exerciseModel.OpponentAttackDefense, Foothold: "sw", Band: 6, Goal: []string{"recon"}, Operates: []string{"web"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestADocumentWithoutOpponentsImportsAsBefore(t *testing.T) {
	got, err := parseOpponents(t, "")
	if err != nil || got != nil {
		t.Fatalf("old documents carry no opponents: %v %+v", err, got)
	}
	if got, err = parseOpponents(t, "[]"); err != nil || len(got) != 0 {
		t.Fatalf("an empty list is none: %v %+v", err, got)
	}
}

func TestEachOpponentFieldIsCheckedAgainstItsVariantAndNamedWithItsPath(t *testing.T) {
	cases := []struct{ name, opponents, field, reason string }{
		{"role", `[{"role":"sniper","band":1,"goal":["recon"]}]`, "opponents[1].role", exerciseModel.OpponentReasonRole},
		{"no role", `[{"band":1,"goal":["recon"]}]`, "opponents[1].role", exerciseModel.OpponentReasonRole},
		{"band low", `[{"role":"attack","foothold":"sw","band":0,"goal":["recon"]}]`, "opponents[1].band", exerciseModel.OpponentReasonBand},
		{"band high", `[{"role":"attack","foothold":"sw","band":7,"goal":["recon"]}]`, "opponents[1].band", exerciseModel.OpponentReasonBand},
		{"goal empty", `[{"role":"attack","foothold":"sw","band":1,"goal":[]}]`, "opponents[1].goal", exerciseModel.OpponentReasonGoalEmpty},
		{"goal missing", `[{"role":"attack","foothold":"sw","band":1}]`, "opponents[1].goal", exerciseModel.OpponentReasonGoalEmpty},
		{"goal unknown", `[{"role":"attack","foothold":"sw","band":1,"goal":["recon","nowhere"]}]`, "opponents[1].goal[2]", exerciseModel.OpponentReasonGoalUnknown},
		{"goal repeated", `[{"role":"attack","foothold":"sw","band":1,"goal":["recon","recon"]}]`, "opponents[1].goal[2]", exerciseModel.OpponentReasonGoalRepeated},
		{"start unknown", `[{"role":"attack","foothold":"sw","band":1,"goal":["recon"],"start":{"after":"nowhere"}}]`, "opponents[1].start.after", exerciseModel.OpponentReasonStartUnknown},
		{"defense without operates", `[{"role":"defense","band":1,"goal":["recon"]}]`, "opponents[1].operates", exerciseModel.OpponentReasonOperatesNeeded},
		{"defense with empty operates", `[{"role":"attack-defense","foothold":"sw","band":1,"goal":["recon"],"operates":[]}]`, "opponents[1].operates", exerciseModel.OpponentReasonOperatesNeeded},
		{"attack with operates", `[{"role":"attack","foothold":"sw","band":1,"goal":["recon"],"operates":["web"]}]`, "opponents[1].operates", exerciseModel.OpponentReasonOperatesForbid},
		{"operates unknown", `[{"role":"defense","band":1,"goal":["recon"],"operates":["web","mail"]}]`, "opponents[1].operates[2]", exerciseModel.OpponentReasonOperatesUnknown},
		{"operates repeated", `[{"role":"defense","band":1,"goal":["recon"],"operates":["web","web"]}]`, "opponents[1].operates[2]", exerciseModel.OpponentReasonOperatesRepeated},
		{"second opponent", `[{"role":"attack","foothold":"sw","band":1,"goal":["recon"]},{"role":"attack","foothold":"sw","band":9,"goal":["recon"]}]`, "opponents[2].band", exerciseModel.OpponentReasonBand},
		{"brief", `[{"role":"attack","foothold":"sw","band":1,"goal":["recon"],"brief":"` + strings.Repeat("я", 2001) + `"}]`, "opponents[1].brief", exerciseModel.OpponentReasonBriefTooLong},
		{"too many", `[` + strings.TrimSuffix(strings.Repeat(`{"role":"attack","foothold":"sw","band":1,"goal":["recon"]},`, 9), ",") + `]`, "opponents", exerciseModel.OpponentReasonCount},
	}
	for _, c := range cases {
		_, err := parseOpponents(t, c.opponents)
		if !errors.Is(err, exerciseModel.ErrOpponentInvalid.Err()) {
			t.Errorf("%s: got %v", c.name, err)
			continue
		}
		ctx := publicContext(t, err)
		if ctx["field"] != c.field || ctx["reason"] != c.reason {
			t.Errorf("%s: field=%v reason=%v, want %s / %s", c.name, ctx["field"], ctx["reason"], c.field, c.reason)
		}
		if ctx["variant"] != 1 || ctx["path"] != "versions[1].variants[1]" {
			t.Errorf("%s: the variant is named: %v", c.name, ctx)
		}
	}
}

func TestABriefOfExactlyTheCapIsAccepted(t *testing.T) {
	if _, err := parseOpponents(t, `[{"role":"attack","foothold":"sw","band":1,"goal":["recon"],"brief":"`+strings.Repeat("я", 2000)+`"}]`); err != nil {
		t.Fatal(err)
	}
}

func TestTheImageToolsetKeysAndBudgetAreTheAuthorsToNeverSet(t *testing.T) {
	for _, key := range []string{"image", "toolset", "tools", "key", "private_key", "budget", "actions", "tokens", "time", "model"} {
		_, err := parseOpponents(t, `[{"role":"attack","foothold":"sw","band":1,"goal":["recon"]},{"role":"attack","foothold":"sw","band":1,"goal":["recon"],"`+key+`":"x"}]`)
		if !errors.Is(err, exerciseModel.ErrOpponentInvalid.Err()) {
			t.Errorf("%s: got %v", key, err)
			continue
		}
		ctx := publicContext(t, err)
		if ctx["field"] != "opponents[2]."+key || ctx["reason"] != exerciseModel.OpponentReasonForbiddenField || ctx["path"] != "versions[1].variants[1]" {
			t.Errorf("%s: %v", key, ctx)
		}
	}
}

func TestAnyOtherUnknownKeyOfAnOpponentIsRefusedByTheStrictDocument(t *testing.T) {
	_, err := parseOpponents(t, `[{"role":"attack","foothold":"sw","band":1,"goal":["recon"],"mood":"angry"}]`)
	if !errors.Is(err, exerciseModel.ErrArchiveInvalid.Err()) || !strings.Contains(publicContext(t, err)["reason"].(string), "mood") {
		t.Fatalf("got %v", err)
	}
}

func TestAStartThatIsNotImmediateOrAfterIsRefused(t *testing.T) {
	for _, start := range []string{`"later"`, `5`, `{}`, `{"after":""}`, `{"after":"recon","soon":true}`, `["recon"]`} {
		_, err := parseOpponents(t, `[{"role":"attack","foothold":"sw","band":1,"goal":["recon"],"start":`+start+`}]`)
		if err == nil {
			t.Errorf("start %s was accepted", start)
		}
	}
}

func TestALinkedVariantTakesItsBasesOpponentsAndMayNotNameItsOwn(t *testing.T) {
	payload := v2Header + `"exercise":{"name":"S"},"versions":[{"variants":[{"tasks":[{"key":"recon","name":"R","check":{"answer":{"type":"flag","values":["ICE{a}"]}}}],"topology":{"devices":[{"name":"web","type":"container"}]}},` +
		`{"linked_to":1,"opponents":[{"role":"attack","foothold":"sw","band":1,"goal":["recon"]}]}]}]}`
	if _, err := decodeArchiveV2([]byte(payload)); !errors.Is(err, exerciseModel.ErrArchiveLinkedVariantField.Err()) {
		t.Fatalf("got %v", err)
	}
}

func TestOpponentsSurviveTheExportAndImportUnchanged(t *testing.T) {
	first, err := parseOpponents(t, validOpponents)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := decodeArchiveV2([]byte(opponentDoc(validOpponents)))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := doc.toParsed(nil)
	if err != nil {
		t.Fatal(err)
	}
	exported := exportVariantV2(parsed.Versions[0].Variants[0], nil, nil)
	again, err := json.Marshal(archiveDocV2{
		Schema: archiveSchema10, Exercise: archiveExerciseV2{Name: "Sample"},
		Versions: []archiveVersionV2{{Variants: []archiveVariantV2{exported}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	reread, err := decodeArchiveV2(again)
	if err != nil {
		t.Fatalf("the exported document is read again: %v\n%s", err, again)
	}
	reparsed, err := reread.toParsed(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := reparsed.Versions[0].Variants[0].Opponents; !reflect.DeepEqual(got, first) {
		t.Fatalf("round trip lost something:\n got %+v\nwant %+v\n%s", got, first, again)
	}
	if !strings.Contains(string(again), `"start":"immediate"`) || !strings.Contains(string(again), `"start":{"after":"recon"}`) {
		t.Fatalf("the written forms of start: %s", again)
	}
}

func TestAVariantWithoutOpponentsExportsWithoutTheKey(t *testing.T) {
	doc, _ := decodeArchiveV2([]byte(opponentDoc("")))
	parsed, _ := doc.toParsed(nil)
	raw, _ := json.Marshal(exportVariantV2(parsed.Versions[0].Variants[0], nil, nil))
	if strings.Contains(string(raw), "opponents") {
		t.Fatalf("old shape kept: %s", raw)
	}
}
