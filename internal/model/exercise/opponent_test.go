package exerciseModel

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/gofrs/uuid"
)

func opponentVariant(opponents ...Opponent) Variant {
	return Variant{
		Tasks: []Task{{Key: "recon", Name: "Recon"}, {Key: "root", Name: "Root"}},
		Topology: Topology{Devices: []Device{
			{ID: uuid.FromStringOrNil("11111111-1111-4111-8111-111111111111"), Name: "web"},
			{ID: uuid.FromStringOrNil("22222222-2222-4222-8222-222222222222"), Name: "db"},
			{ID: uuid.FromStringOrNil("33333333-3333-4333-8333-333333333333"), Name: "sw", Type: DeviceTypeUnmanagedSwitch},
		}},
		Opponents: opponents,
	}
}

func fieldOf(t *testing.T, err error) (field, reason string) {
	t.Helper()
	if !errors.Is(err, ErrOpponentInvalid.Err()) {
		t.Fatalf("got %v", err)
	}
	var withContext interface{ PublicContext() map[string]any }
	if !errors.As(err, &withContext) {
		t.Fatal("no public context")
	}
	ctx := withContext.PublicContext()
	return ctx["field"].(string), ctx["reason"].(string)
}

func TestAVariantWithoutOpponentsIsValid(t *testing.T) {
	if err := opponentVariant().ValidateOpponents(); err != nil {
		t.Fatal(err)
	}
}

func TestTheRolesDecideWhetherAnOpponentOperatesDevices(t *testing.T) {
	good := []Opponent{
		{Role: OpponentAttack, Foothold: "sw", Band: 1, Goal: []string{"root"}},
		{Role: OpponentDefense, Band: 6, Goal: []string{"recon", "root"}, Operates: []string{"web"}, Start: OpponentStart{After: "recon"}},
		{Role: OpponentAttackDefense, Foothold: "sw", Band: 3, Goal: []string{"root"}, Operates: []string{"db", "web"}, Brief: "hold the line"},
	}
	if err := opponentVariant(good...).ValidateOpponents(); err != nil {
		t.Fatal(err)
	}
	if field, reason := fieldOf(t, opponentVariant(Opponent{Role: OpponentAttack, Foothold: "sw", Band: 1, Goal: []string{"root"}, Operates: []string{"web"}}).ValidateOpponents()); field != "opponents[1].operates" || reason != OpponentReasonOperatesForbid {
		t.Fatalf("%s %s", field, reason)
	}
	if field, reason := fieldOf(t, opponentVariant(good[0], Opponent{Role: OpponentDefense, Band: 1, Goal: []string{"root"}}).ValidateOpponents()); field != "opponents[2].operates" || reason != OpponentReasonOperatesNeeded {
		t.Fatalf("%s %s", field, reason)
	}
}

func TestAnOpponentThatNamesATaskOrDeviceTheVariantLacksIsInvalid(t *testing.T) {
	for _, c := range []struct {
		opponent Opponent
		field    string
	}{
		{Opponent{Role: OpponentAttack, Foothold: "sw", Band: 1, Goal: []string{"gone"}}, "opponents[1].goal[1]"},
		{Opponent{Role: OpponentAttack, Foothold: "sw", Band: 1, Goal: []string{"root"}, Start: OpponentStart{After: "gone"}}, "opponents[1].start.after"},
		{Opponent{Role: OpponentDefense, Band: 1, Goal: []string{"root"}, Operates: []string{"gone"}}, "opponents[1].operates[1]"},
	} {
		if field, _ := fieldOf(t, opponentVariant(c.opponent).ValidateOpponents()); field != c.field {
			t.Errorf("field = %q, want %q", field, c.field)
		}
	}
}

func TestADocumentValidationOfAVariantChecksItsOpponents(t *testing.T) {
	v := opponentVariant(Opponent{Role: OpponentAttack, Foothold: "sw", Band: 9, Goal: []string{"root"}})
	v.Tasks[0].Check = Check{Answer: &AnswerCheck{Type: AnswerTypeFlag}}
	v.Tasks[1].Check = Check{Answer: &AnswerCheck{Type: AnswerTypeFlag}}
	if field, _ := fieldOf(t, v.ValidateDocument()); field != "opponents[1].band" {
		t.Fatalf("field = %q", field)
	}
}

func TestStartIsWrittenAsImmediateOrAfter(t *testing.T) {
	immediate, _ := json.Marshal(Opponent{Role: OpponentAttack, Foothold: "sw", Band: 1, Goal: []string{"a"}})
	after, _ := json.Marshal(Opponent{Role: OpponentAttack, Foothold: "sw", Band: 1, Goal: []string{"a"}, Start: OpponentStart{After: "b"}})
	if !strings.Contains(string(immediate), `"start":"immediate"`) || !strings.Contains(string(after), `"start":{"after":"b"}`) {
		t.Fatalf("%s %s", immediate, after)
	}
	for _, raw := range []string{string(immediate), string(after)} {
		var back Opponent
		if err := json.Unmarshal([]byte(raw), &back); err != nil {
			t.Fatal(err)
		}
		again, _ := json.Marshal(back)
		if string(again) != raw {
			t.Fatalf("not lossless: %s -> %s", raw, again)
		}
	}
}

func TestOpponentsAreStoredWithTheVariantAndSurviveACopy(t *testing.T) {
	v := opponentVariant(Opponent{Role: OpponentDefense, Band: 2, Goal: []string{"root"}, Operates: []string{"web"}, Brief: "b"})
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var back Variant
	if err = json.Unmarshal(raw, &back); err != nil || !reflect.DeepEqual(back.Opponents, v.Opponents) {
		t.Fatalf("stored form: %v %+v", err, back.Opponents)
	}
	// A variant saved before opponents existed has no such key.
	plain, _ := json.Marshal(opponentVariant())
	if strings.Contains(string(plain), "opponents") {
		t.Fatalf("old shape kept: %s", plain)
	}
	copied := CopyOpponents(v.Opponents)
	copied[0].Goal[0], copied[0].Operates[0] = "changed", "changed"
	if v.Opponents[0].Goal[0] != "root" || v.Opponents[0].Operates[0] != "web" {
		t.Fatal("a copy must not share the lists")
	}
}

func TestALinkedVariantTakesItsBasesOpponents(t *testing.T) {
	base := opponentVariant(Opponent{Role: OpponentAttack, Foothold: "sw", Band: 4, Goal: []string{"root"}})
	base.ID = uuid.FromStringOrNil("aaaaaaaa-0000-4000-8000-000000000001")
	linked := Variant{ID: uuid.FromStringOrNil("aaaaaaaa-0000-4000-8000-000000000002"), LinkedTo: uuid.NullUUID{UUID: base.ID, Valid: true}}
	version := ExerciseVersion{Variants: []Variant{base, linked}}
	if err := version.SyncLinkedVariants(); err != nil {
		t.Fatal(err)
	}
	if got := version.Variants[1].Opponents; len(got) != 1 || got[0].Band != 4 || got[0].Goal[0] != "root" {
		t.Fatalf("linked variant opponents: %+v", got)
	}
	version.Variants[1].Opponents[0].Goal[0] = "changed"
	if version.Variants[0].Opponents[0].Goal[0] != "root" {
		t.Fatal("the linked variant shares the base's lists")
	}
}

func TestWithoutOpponentsLeavesTheRestOfTheVariant(t *testing.T) {
	v := opponentVariant(Opponent{Role: OpponentAttack, Foothold: "sw", Band: 1, Goal: []string{"root"}})
	stripped := v.WithoutOpponents()
	if stripped.Opponents != nil || len(stripped.Tasks) != 2 || len(v.Opponents) != 1 {
		t.Fatalf("%+v / %+v", stripped, v)
	}
}

func TestTheFootholdIsARealSegmentForAttackersOnly(t *testing.T) {
	for name, c := range map[string]struct {
		opponent Opponent
		reason   string
	}{
		"an attacker without one":     {Opponent{Role: OpponentAttack, Band: 1, Goal: []string{"root"}}, OpponentReasonFootholdNeeded},
		"an attack-defender without":  {Opponent{Role: OpponentAttackDefense, Band: 1, Goal: []string{"root"}, Operates: []string{"web"}}, OpponentReasonFootholdNeeded},
		"a defender with one":         {Opponent{Role: OpponentDefense, Band: 1, Goal: []string{"root"}, Operates: []string{"web"}, Foothold: "sw"}, OpponentReasonFootholdForbid},
		"a name that is no segment":   {Opponent{Role: OpponentAttack, Band: 1, Goal: []string{"root"}, Foothold: "gone"}, OpponentReasonFootholdUnknown},
		"a device that is no segment": {Opponent{Role: OpponentAttack, Band: 1, Goal: []string{"root"}, Foothold: "web"}, OpponentReasonFootholdUnknown},
	} {
		field, reason := fieldOf(t, opponentVariant(c.opponent).ValidateOpponents())
		if field != "opponents[1].foothold" || reason != c.reason {
			t.Errorf("%s: %s %s", name, field, reason)
		}
	}
}
