package auth_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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
	return f.forgetDraftErr
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
	req.Header.Set("Origin", "https://exercises.example.test")
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

// Only the catalog and the admin site hold a catalog editor: an event site (or any other platform site) never gets a
// draft key, even with a session cookie the browser sends along.
func TestDraftKey_OnlyTheCatalogAndAdminOriginsGetAKey(t *testing.T) {
	for _, tc := range []struct {
		name, origin, referer string
		want                  int
	}{
		{"catalog", "https://exercises.example.test", "", http.StatusOK},
		{"admin", "https://admin.example.test", "", http.StatusOK},
		{"catalog referer without origin", "", "https://exercises.example.test/exercises/x/edit", http.StatusOK},
		{"event site", "https://ctf.example.test", "", http.StatusForbidden},
		{"event site referer", "", "https://ctf.example.test/", http.StatusForbidden},
		{"id site", "https://id.example.test", "", http.StatusForbidden},
		{"main site", "https://example.test", "", http.StatusForbidden},
		{"plain http catalog", "http://exercises.example.test", "", http.StatusForbidden},
		{"lookalike", "https://exercises.example.test.evil.test", "", http.StatusForbidden},
		{"event site origin, catalog referer", "https://ctf.example.test", "https://exercises.example.test/", http.StatusForbidden},
		{"neither origin nor referer", "", "", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			uc := &fakeUC{draftKey: draftKeyModel.Key{ID: testDraftID, Key: bytes.Repeat([]byte{5}, 32)}}
			r := passkeyRouter(uc, &fakeProt{}, true)
			req := httptest.NewRequest(http.MethodPost, "/api/me/draft-key", nil)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.referer != "" {
				req.Header.Set("Referer", tc.referer)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("want %d, got %d: %s", tc.want, w.Code, w.Body)
			}
			if tc.want != http.StatusOK && (uc.draftCalls != 0 || len(w.Result().Cookies()) != 0) {
				t.Fatalf("a refused origin reached the use case (%d calls) or got a cookie", uc.draftCalls)
			}
		})
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

// The sessions are already revoked when the draft keys are deleted: a failure there is logged, never a 500.
func TestRevokeOtherSessions_ADraftKeyFailureIsNotAnError(t *testing.T) {
	uc := &fakeUC{forgetDraftErr: errors.New("database down")}
	r := passkeyRouter(uc, &fakeProt{}, true)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/auth/sessions", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
}

// A password change signs the other browsers out: their draft keys go, this browser keeps its own; a failure of that
// delete does not undo the answer of a change that is done.
func TestChangePassword_ForgetsTheOtherBrowsersDraftKeys(t *testing.T) {
	for _, forgetErr := range []error{nil, errors.New("database down")} {
		uc := &fakeUC{forgetDraftErr: forgetErr}
		r := passkeyRouter(uc, &fakeProt{}, true)
		req := httptest.NewRequest(http.MethodPost, "/api/auth/password/change",
			strings.NewReader(`{"OldPassword":"Old!1234","NewPassword":"New!12345"}`))
		req.AddCookie(&http.Cookie{Name: draftKeyModel.CookieName, Value: testDraftID})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK || uc.keptDraft != testDraftID {
			t.Fatalf("change (%v): %d, kept %q: %s", forgetErr, w.Code, uc.keptDraft, w.Body)
		}
	}
	// A failed change signs nobody out: no key is touched.
	uc := &fakeUC{changeErr: errors.New("wrong password"), keptDraft: "untouched"}
	r := passkeyRouter(uc, &fakeProt{}, true)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/auth/password/change",
		strings.NewReader(`{"OldPassword":"x","NewPassword":"y"}`)))
	if uc.keptDraft != "untouched" {
		t.Fatalf("a failed change deleted draft keys")
	}
}
