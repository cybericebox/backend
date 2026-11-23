package secondFactorModel_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid"

	secondFactorModel "github.com/cybericebox/backend/internal/model/secondFactor"
)

func TestNewRecoveryCodesAreTenDistinctReadableCodes(t *testing.T) {
	codes, err := secondFactorModel.NewRecoveryCodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != secondFactorModel.RecoveryCodeCount {
		t.Fatalf("got %d codes, want %d", len(codes), secondFactorModel.RecoveryCodeCount)
	}
	seen := map[string]bool{}
	for _, c := range codes {
		if len(c) != 14 || c[4] != '-' || c[9] != '-' {
			t.Errorf("code %q is not in the XXXX-XXXX-XXXX form", c)
		}
		if strings.ContainsAny(c, "ILOU") {
			t.Errorf("code %q holds a letter people confuse with a digit", c)
		}
		if seen[c] {
			t.Errorf("code %q repeats", c)
		}
		seen[c] = true
	}
}

func TestRecoveryCodeHashIgnoresHowItIsTyped(t *testing.T) {
	user := uuid.Must(uuid.NewV7())
	h := secondFactorModel.HashRecoveryCode(user, "AB2C-D3EF-G4HJ")
	for _, typed := range []string{"ab2c-d3ef-g4hj", " AB2C D3EF G4HJ ", "AB2CD3EFG4HJ"} {
		if !bytes.Equal(secondFactorModel.HashRecoveryCode(user, typed), h) {
			t.Errorf("%q hashes differently", typed)
		}
	}
	// O for 0, I and L for 1: the letters the alphabet leaves out read as the digits they look like.
	if !bytes.Equal(secondFactorModel.HashRecoveryCode(user, "1000-AAAA-AAAA"), secondFactorModel.HashRecoveryCode(user, "IOOO-AAAA-AAAA")) {
		t.Error("I and O are not read as 1 and 0")
	}
	if bytes.Equal(secondFactorModel.HashRecoveryCode(uuid.Must(uuid.NewV7()), "AB2C-D3EF-G4HJ"), h) {
		t.Error("the same code of another person hashes the same")
	}
}

func TestStepUpFreshness(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	s := secondFactorModel.StepUp{At: now.Add(-4 * time.Minute), Method: secondFactorModel.MethodTOTP}
	if !s.Fresh(now, secondFactorModel.StepUpWindow) {
		t.Error("a step-up of 4 minutes ago is fresh")
	}
	s.At = now.Add(-6 * time.Minute)
	if s.Fresh(now, secondFactorModel.StepUpWindow) {
		t.Error("a step-up of 6 minutes ago is not fresh")
	}
	if (secondFactorModel.StepUp{}).Fresh(now, secondFactorModel.StepUpWindow) {
		t.Error("no step-up is not fresh")
	}
	if (secondFactorModel.StepUp{At: now.Add(time.Minute), Method: secondFactorModel.MethodTOTP}).Fresh(now, secondFactorModel.StepUpWindow) {
		t.Error("a step-up in the future (clock skew) is not fresh")
	}
}

func TestOnlyPasskeyAndTOTPAreSecondFactors(t *testing.T) {
	for m, want := range map[secondFactorModel.Method]bool{
		secondFactorModel.MethodPasskey: true, secondFactorModel.MethodTOTP: true,
		secondFactorModel.MethodPassword: false, secondFactorModel.MethodRecentSignIn: false, secondFactorModel.MethodRecoveryCode: false,
		"google": false, "email-code": false, "sms": false,
	} {
		if got := m.IsSecondFactor(); got != want {
			t.Errorf("%s.IsSecondFactor() = %v, want %v", m, got, want)
		}
	}
	// A recovery code stands in for a lost factor when the person manages their factors, nowhere else.
	if !secondFactorModel.MethodRecoveryCode.ConfirmsFactorChange() || secondFactorModel.MethodPassword.ConfirmsFactorChange() {
		t.Error("factor changes are confirmed by a second factor or a recovery code only")
	}
}
