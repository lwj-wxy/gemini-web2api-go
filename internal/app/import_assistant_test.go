package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func importTestRequest(t *testing.T, email, job string, proxyID int64, proxyURL string) *http.Request {
	t.Helper()
	digest := sha256.Sum256([]byte(proxyURL))
	body, _ := json.Marshal(map[string]interface{}{"email": email, "job_id": job, "cookie": "SAPISID=bulk-sapisid; __Secure-1PSID=bulk-" + job, "proxy_id": proxyID, "proxy_url_hash": hex.EncodeToString(digest[:])})
	r := httptest.NewRequest("POST", "https://gemini.example/admin/api/cookies/import-verified", strings.NewReader(string(body)))
	r.Header.Set("X-Gemini-Import", "1")
	r.Header.Set("X-Import-Assistant-Key", "test-service-key")
	return r
}

func TestVerifiedImportIsAtomicAndIdempotent(t *testing.T) {
	t.Setenv("GEMINI_IMPORT_ASSISTANT_KEY", "test-service-key")
	original := verifyImportCookie
	t.Cleanup(func() { verifyImportCookie = original })
	job := "2223456789abcdef0123456789abcdef"
	verifyImportCookie = func(cookie, proxy string) error { return errors.New("rejected") }
	w := httptest.NewRecorder()
	handleVerifiedCookieImport(w, importTestRequest(t, "bulk-test@example.com", job, 0, ""))
	if w.Code != 422 {
		t.Fatalf("unverified import status=%d", w.Code)
	}
	for _, a := range accountList() {
		if a.Note == "批量登录导入:"+job {
			t.Fatal("unverified account was inserted")
		}
	}
	calls := 0
	verifyImportCookie = func(cookie, proxy string) error {
		calls++
		if proxy != "" {
			t.Error("changed egress")
		}
		return nil
	}
	var first int64
	for index := 0; index < 2; index++ {
		w = httptest.NewRecorder()
		handleVerifiedCookieImport(w, importTestRequest(t, "bulk-test@example.com", job, 0, ""))
		if w.Code != 200 {
			t.Fatalf("verified import status=%d body=%s", w.Code, w.Body.String())
		}
		var result struct {
			ID int64 `json:"id"`
		}
		json.Unmarshal(w.Body.Bytes(), &result)
		if index == 0 {
			first = result.ID
			t.Cleanup(func() { accountDelete(first) })
		} else if result.ID != first {
			t.Fatal("repeated import duplicated account")
		}
	}
	if calls != 1 {
		t.Fatal("completed import repeated verification")
	}
	account := accountByID(first)
	if account.Status != "enabled" || account.LastOkAt == 0 || account.ProxyID != 0 {
		t.Fatal("insert was not verified and bound")
	}
}

func TestVerifiedImportPreservesExistingAccount(t *testing.T) {
	t.Setenv("GEMINI_IMPORT_ASSISTANT_KEY", "test-service-key")
	original := verifyImportCookie
	verifyImportCookie = func(cookie, proxy string) error { return nil }
	t.Cleanup(func() { verifyImportCookie = original })
	id, err := accountAdd("bulk-existing@example.com", "SAPISID=old; __Secure-1PSID=old-session", "keep-note")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { accountDelete(id) })
	accountSetStatus(id, "disabled")
	w := httptest.NewRecorder()
	handleVerifiedCookieImport(w, importTestRequest(t, "bulk-existing@example.com", "3223456789abcdef0123456789abcdef", 0, ""))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"duplicate":true`) {
		t.Fatalf("existing account status=%d", w.Code)
	}
	account := accountByID(id)
	if account.Cookie != "SAPISID=old; __Secure-1PSID=old-session" || account.Status != "disabled" || account.Note != "keep-note" {
		t.Fatal("existing account overwritten")
	}
}

func TestAssistantProxyDoesNotForwardAdminCredentials(t *testing.T) {
	originalToken := cfg.AdminToken
	cfg.AdminToken = "test-admin"
	t.Cleanup(func() { cfg.AdminToken = originalToken })
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.URL.RawQuery != "" {
			t.Error("admin credentials forwarded")
		}
		if r.URL.Path != "/api/jobs" || r.Header.Get("X-Import-Assistant-Key") != "test-service-key" {
			t.Error("invalid assistant proxy mapping")
		}
		w.Write([]byte(`{"items":[]}`))
	}))
	defer target.Close()
	t.Setenv("GEMINI_IMPORT_ASSISTANT_URL", target.URL)
	t.Setenv("GEMINI_IMPORT_ASSISTANT_KEY", "test-service-key")
	r := httptest.NewRequest("GET", "https://gemini.example/admin/api/import-assistant/jobs?token=private", nil)
	r.Header.Set("Cookie", "gemini_admin_session=private")
	r.Header.Set("Authorization", "Bearer test-admin")
	w := httptest.NewRecorder()
	handleImportAssistant(w, r)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("invalid assistant response")
	}
	r = httptest.NewRequest("GET", "https://gemini.example/admin/api/import-assistant/jobs", nil)
	w = httptest.NewRecorder()
	requireAuth(handleImportAssistant)(w, r)
	if w.Code != 401 {
		t.Fatal("assistant accessible without admin authentication")
	}
}

func TestAssistantMutationsRequireHeaderAndSameOrigin(t *testing.T) {
	for _, item := range []struct {
		origin, header string
		want           bool
	}{
		{"https://gemini.example", "1", true},
		{"https://attacker.example", "1", false},
		{"http://gemini.example", "1", false},
		{"https://gemini.example", "", false},
		{"", "1", true},
	} {
		r := httptest.NewRequest("POST", "https://gemini.example/admin/api/import-assistant/jobs", nil)
		r.Header.Set("Origin", item.origin)
		r.Header.Set("X-Gemini-Import", item.header)
		if assistantMutationAllowed(r) != item.want {
			t.Fatal("invalid origin policy")
		}
	}
}

func TestAssistantProxyFailsClosedWhenNotConfigured(t *testing.T) {
	t.Setenv("GEMINI_IMPORT_ASSISTANT_URL", "")
	r := httptest.NewRequest("GET", "https://gemini.example/admin/import-assistant/", nil)
	w := httptest.NewRecorder()
	handleImportAssistant(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestVerifiedImportRejectsInvalidCookieBeforeAnyNetwork(t *testing.T) {
	t.Setenv("GEMINI_IMPORT_ASSISTANT_KEY", "test-service-key")
	r := httptest.NewRequest("POST", "https://gemini.example/admin/api/cookies/import-verified", strings.NewReader(`{"email":"test@example.com","cookie":"SAPISID=x","job_id":"0123456789abcdef0123456789abcdef"}`))
	r.Header.Set("X-Gemini-Import", "1")
	r.Header.Set("X-Import-Assistant-Key", "test-service-key")
	w := httptest.NewRecorder()
	handleVerifiedCookieImport(w, r)
	if w.Code != 400 {
		t.Fatalf("status=%d, body=%s", w.Code, w.Body.String())
	}
}
