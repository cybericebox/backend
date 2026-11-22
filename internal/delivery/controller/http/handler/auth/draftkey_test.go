package auth_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofrs/uuid"

	draftKeyModel "github.com/cybericebox/backend/internal/model/draftKey"
)

func (f *fakeUC) DraftKey(_ context.Context, _ uuid.UUID, cookieID string) (draftKeyModel.Key, error) {
	f.draftCalls++
	f.draftCookie = cookieID
	return f.draftKey, nil
}

func (f *fakeUC) ForgetDraftKey(_ context.Context, _ uuid.UUID, cookieID string) error {
	f.forgotDraft = cookieID
	return nil
}

func (f *fakeUC) ForgetOtherDraftKeys(_ context.Context, _ uuid.UUID, cookieID string) error {
	f.keptDraft = cookieID
	return nil
}

const testDraftID = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func draftCookieOf(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == draftKeyModel.CookieName {
			return c
		}
	}
	t.Fatalf("no %s cookie in %v", draftKeyModel.CookieName, w.Header().Values("Set-Cookie"))
	return nil
}

func TestDraftKey_NeedsASession(t *testing.T) {
	uc := &fakeUC{}
	r := passkeyRouter(uc, &fakeProt{}, false)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/me/draft-key", nil))
	if w.Code != http.StatusUnauthorized || uc.draftCalls != 0 {
		t.Fatalf("want 401 and no key, got %d (%d calls)", w.Code, uc.draftCalls)
	}
}

func TestDraftKey_ReturnsTheKeyAndSetsTheBrowserCookie(t *testing.T) {
	key := bytes.Repeat([]byte{5}, 32)
	uc := &fakeUC{draftKey: draftKeyModel.Key{ID: testDraftID, Key: key}}
	r := passkeyRouter(uc, &fakeProt{}, true)

	req := httptest.NewRequest(http.MethodPost, "/api/me/draft-key", nil)
	req.AddCookie(&http.Cookie{Name: draftKeyModel.CookieName, Value: "previous-id"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	if uc.draftCookie != "previous-id" {
		t.Fatalf("the use case got cookie %q", uc.draftCookie)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control %q", got)
	}
	var env struct {
		Data struct {
			ID  string
			Key string
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(env.Data.Key)
	if err != nil || env.Data.ID != testDraftID || !bytes.Equal(raw, key) {
		t.Fatalf("body %s (%v)", w.Body, err)
	}
	c := draftCookieOf(t, w)
	if c.Value != testDraftID || c.Path != draftKeyModel.CookiePath || c.MaxAge != int(draftKeyModel.TTL.Seconds()) ||
		!c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie %+v", c)
	}
}

func TestSignOut_ForgetsThisBrowsersDraftKeyAndClearsTheCookie(t *testing.T) {
	uc := &fakeUC{}
	r := passkeyRouter(uc, &fakeProt{}, true)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/sign-out", nil)
	req.AddCookie(&http.Cookie{Name: draftKeyModel.CookieName, Value: testDraftID})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || uc.forgotDraft != testDraftID {
		t.Fatalf("sign-out %d, forgot %q", w.Code, uc.forgotDraft)
	}
	if c := draftCookieOf(t, w); c.MaxAge >= 0 || c.Path != draftKeyModel.CookiePath {
		t.Fatalf("cookie not cleared: %+v", c)
	}
}

func TestRevokeOtherSessions_KeepsOnlyThisBrowsersDraftKey(t *testing.T) {
	uc := &fakeUC{}
	r := passkeyRouter(uc, &fakeProt{}, true)
	req := httptest.NewRequest(http.MethodDelete, "/api/auth/sessions", nil)
	req.AddCookie(&http.Cookie{Name: draftKeyModel.CookieName, Value: testDraftID})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || uc.keptDraft != testDraftID {
		t.Fatalf("revoke others %d, kept %q", w.Code, uc.keptDraft)
	}
}
