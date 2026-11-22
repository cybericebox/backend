package auth_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/jackc/pgx/v5"
	"go.uber.org/mock/gomock"

	"github.com/cybericebox/backend/internal/delivery/repository/postgres"
	postgresMocks "github.com/cybericebox/backend/internal/delivery/repository/postgres/mocks"
	draftKeyModel "github.com/cybericebox/backend/internal/model/draftKey"
	"github.com/cybericebox/backend/internal/secretctx"
	"github.com/cybericebox/backend/internal/useCase/auth"
	"github.com/cybericebox/backend/pkg/secret"
)

func newDraftKeyUC(t *testing.T) (*auth.AuthUseCase, *postgresMocks.MockQuerier, *secret.Cipher) {
	t.Helper()
	uc, repo := newExportUC(t)
	cipher, err := secret.NewFromBytes(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	uc.SetDraftKeyCipher(cipher)
	return uc, repo, cipher
}

// nearStale: the stale bound of a query is "now minus the key lifetime".
func nearStale(t *testing.T, got time.Time) {
	t.Helper()
	want := time.Now().Add(-draftKeyModel.TTL)
	if d := got.Sub(want); d > 5*time.Second || d < -5*time.Second {
		t.Fatalf("stale bound %v, want about %v", got, want)
	}
}

// expectNewKey stubs the creation of a key for uid and returns what was stored.
func expectNewKey(t *testing.T, repo *postgresMocks.MockQuerier, uid uuid.UUID) *postgres.InsertUserDraftKeyParams {
	t.Helper()
	var stored postgres.InsertUserDraftKeyParams
	repo.EXPECT().InsertUserDraftKey(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, a postgres.InsertUserDraftKeyParams) error {
			if a.UserID != uid {
				t.Errorf("key stored for %s, want %s", a.UserID, uid)
			}
			stored = a
			return nil
		})
	repo.EXPECT().TrimUserDraftKeys(gomock.Any(), postgres.TrimUserDraftKeysParams{UserID: uid, Keep: draftKeyModel.MaxPerUser}).
		Return(int64(0), nil)
	return &stored
}

func expectStaleSweep(t *testing.T, repo *postgresMocks.MockQuerier, uid uuid.UUID) {
	t.Helper()
	repo.EXPECT().DeleteStaleUserDraftKeys(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, a postgres.DeleteStaleUserDraftKeysParams) (int64, error) {
			if a.UserID != uid {
				t.Errorf("stale keys swept for %s, want %s", a.UserID, uid)
			}
			nearStale(t, a.StaleBefore)
			return 0, nil
		})
}

func TestDraftKey_CreatesASealedKeyWhenTheBrowserHasNone(t *testing.T) {
	uc, repo, cipher := newDraftKeyUC(t)
	uid := uuid.Must(uuid.NewV7())
	expectStaleSweep(t, repo, uid)
	stored := expectNewKey(t, repo, uid)

	got, err := uc.DraftKey(context.Background(), uid, "")
	if err != nil {
		t.Fatalf("draft key: %v", err)
	}
	if !draftKeyModel.ValidID(got.ID) || len(got.Key) != draftKeyModel.KeySize {
		t.Fatalf("got id %q and a key of %d bytes", got.ID, len(got.Key))
	}
	if stored.ID != got.ID || stored.KeyCiphertext == "" || bytes.Contains([]byte(stored.KeyCiphertext), got.Key) {
		t.Fatalf("stored %+v", stored)
	}
	plain, err := cipher.DecryptWithContext(stored.KeyCiphertext, secretctx.DraftKey(uid, got.ID))
	if err != nil || !bytes.Equal(plain, got.Key) {
		t.Fatalf("the stored key does not open to the returned one: %v", err)
	}
	// Two browsers never share a key.
	expectStaleSweep(t, repo, uid)
	expectNewKey(t, repo, uid)
	other, err := uc.DraftKey(context.Background(), uid, "")
	if err != nil || other.ID == got.ID || bytes.Equal(other.Key, got.Key) {
		t.Fatalf("second browser: %+v %v", other, err)
	}
}

func TestDraftKey_ReusesTheLiveKeyOfThisUser(t *testing.T) {
	uc, repo, cipher := newDraftKeyUC(t)
	uid := uuid.Must(uuid.NewV7())
	id := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	key := bytes.Repeat([]byte{9}, 32)
	sealed, err := cipher.EncryptWithContext(key, secretctx.DraftKey(uid, id))
	if err != nil {
		t.Fatal(err)
	}
	expectStaleSweep(t, repo, uid)
	repo.EXPECT().TouchUserDraftKey(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, a postgres.TouchUserDraftKeyParams) (string, error) {
			if a.ID != id || a.UserID != uid || time.Since(a.UsedAt) > 5*time.Second {
				t.Errorf("touch %+v", a)
			}
			nearStale(t, a.StaleBefore)
			return sealed, nil
		})

	got, err := uc.DraftKey(context.Background(), uid, id)
	if err != nil || got.ID != id || !bytes.Equal(got.Key, key) {
		t.Fatalf("reuse: %+v %v", got, err)
	}
}

// The touch is keyed on the caller: the key of another person (or a stale one) is not found, so a new key is made
// and the other one is never opened or returned.
func TestDraftKey_AnotherUsersOrAStaleIDGetsANewKey(t *testing.T) {
	uc, repo, _ := newDraftKeyUC(t)
	uid := uuid.Must(uuid.NewV7())
	foreign := "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	expectStaleSweep(t, repo, uid)
	repo.EXPECT().TouchUserDraftKey(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, a postgres.TouchUserDraftKeyParams) (string, error) {
			if a.UserID != uid {
				t.Errorf("touch keyed on %s, want the caller %s", a.UserID, uid)
			}
			return "", pgx.ErrNoRows
		})
	stored := expectNewKey(t, repo, uid)

	got, err := uc.DraftKey(context.Background(), uid, foreign)
	if err != nil || got.ID == foreign || stored.ID != got.ID {
		t.Fatalf("got %+v (stored %q): %v", got, stored.ID, err)
	}
}

// A key that no longer opens (another user's row copied, a lost platform key) is replaced, never an error.
func TestDraftKey_AKeyThatDoesNotOpenIsReplaced(t *testing.T) {
	uc, repo, cipher := newDraftKeyUC(t)
	uid := uuid.Must(uuid.NewV7())
	id := "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"
	sealedForOther, _ := cipher.EncryptWithContext(bytes.Repeat([]byte{1}, 32), secretctx.DraftKey(uuid.Must(uuid.NewV7()), id))
	expectStaleSweep(t, repo, uid)
	repo.EXPECT().TouchUserDraftKey(gomock.Any(), gomock.Any()).Return(sealedForOther, nil)
	repo.EXPECT().DeleteUserDraftKey(gomock.Any(), postgres.DeleteUserDraftKeyParams{ID: id, UserID: uid}).Return(int64(1), nil)
	expectNewKey(t, repo, uid)

	got, err := uc.DraftKey(context.Background(), uid, id)
	if err != nil || got.ID == id {
		t.Fatalf("got %+v: %v", got, err)
	}
}

// A malformed cookie is never looked up.
func TestDraftKey_AMalformedCookieIsIgnored(t *testing.T) {
	uc, repo, _ := newDraftKeyUC(t)
	uid := uuid.Must(uuid.NewV7())
	expectStaleSweep(t, repo, uid)
	expectNewKey(t, repo, uid)
	if _, err := uc.DraftKey(context.Background(), uid, "x' OR 1=1"); err != nil {
		t.Fatal(err)
	}
}

func TestDraftKey_WithoutThePlatformKeyNothingIsStored(t *testing.T) {
	uc, _ := newExportUC(t) // no repository call is expected
	if _, err := uc.DraftKey(context.Background(), uuid.Must(uuid.NewV7()), ""); err == nil {
		t.Fatal("want an error without PLATFORM_SECRETS_KEY")
	}
}

func TestForgetDraftKey_DeletesOnlyThisBrowsersKey(t *testing.T) {
	uc, repo, _ := newDraftKeyUC(t)
	uid := uuid.Must(uuid.NewV7())
	id := "DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD"
	repo.EXPECT().DeleteUserDraftKey(gomock.Any(), postgres.DeleteUserDraftKeyParams{ID: id, UserID: uid}).Return(int64(1), nil)
	if err := uc.ForgetDraftKey(context.Background(), uid, id); err != nil {
		t.Fatal(err)
	}
	// No cookie, or a malformed one: nothing to delete.
	if err := uc.ForgetDraftKey(context.Background(), uid, ""); err != nil {
		t.Fatal(err)
	}
	if err := uc.ForgetDraftKey(context.Background(), uid, "short"); err != nil {
		t.Fatal(err)
	}
}

func TestForgetOtherDraftKeys_KeepsOnlyThisBrowsersKey(t *testing.T) {
	uc, repo, _ := newDraftKeyUC(t)
	uid := uuid.Must(uuid.NewV7())
	id := "EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE"
	repo.EXPECT().DeleteUserDraftKeysExcept(gomock.Any(), postgres.DeleteUserDraftKeysExceptParams{UserID: uid, KeepID: id}).Return(int64(2), nil)
	if err := uc.ForgetOtherDraftKeys(context.Background(), uid, id); err != nil {
		t.Fatal(err)
	}
	// Without a key of its own the browser keeps nothing.
	repo.EXPECT().DeleteAllUserDraftKeys(gomock.Any(), uid).Return(int64(2), nil)
	if err := uc.ForgetOtherDraftKeys(context.Background(), uid, ""); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupStaleDraftKeys_DeletesKeysUnusedForTheirLifetime(t *testing.T) {
	uc, repo, _ := newDraftKeyUC(t)
	repo.EXPECT().DeleteStaleDraftKeys(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, before time.Time) (int64, error) {
			nearStale(t, before)
			return 3, nil
		})
	if err := uc.CleanupStaleDraftKeys(context.Background()); err != nil {
		t.Fatal(err)
	}
}
