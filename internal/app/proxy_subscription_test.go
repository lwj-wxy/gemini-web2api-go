package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func subscriptionTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("GEMINI_PROXY_SUBSCRIPTION_DIR", dir)
	old := cfg.AdminToken
	cfg.AdminToken = "private-subscription-fixture"
	t.Cleanup(func() { cfg.AdminToken = old })
	seed := []byte(`{"available":true,"configured":true,"source_host":"subscription.example","state":"idle"}`)
	if err := os.WriteFile(filepath.Join(dir, "proxy-subscription-status.json"), seed, 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func subscriptionCall(t *testing.T, method, body, origin string, auth bool) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "https://gemini.example/admin/api/proxy-subscription", strings.NewReader(body))
	if auth {
		request.Header.Set("Authorization", "Bearer "+cfg.AdminToken)
	}
	request.Header.Set("X-Gemini-Proxy", "1")
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	response := httptest.NewRecorder()
	requireAuth(handleProxySubscription)(response, request)
	return response
}

func TestSubscriptionQueuesPrivateURLAndDoesNotReturnIt(t *testing.T) {
	dir := subscriptionTest(t)
	body := `{"url":"https://subscription.example/sub?token=private-fixture","restore_deleted":false}`
	response := subscriptionCall(t, http.MethodPut, body, "https://gemini.example", true)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if bytes.Contains(response.Body.Bytes(), []byte("private-fixture")) {
		t.Fatal("subscription token leaked")
	}
	queued, err := os.ReadFile(filepath.Join(dir, "proxy-subscription-request.json"))
	if err != nil {
		t.Fatal(err)
	}
	var request map[string]any
	if err = json.Unmarshal(queued, &request); err != nil {
		t.Fatal(err)
	}
	if request["url"] != "https://subscription.example/sub?token=private-fixture" || request["job_id"] == "" {
		t.Fatal("missing queued subscription")
	}
	response = subscriptionCall(t, http.MethodGet, "", "", true)
	if response.Code != 200 || bytes.Contains(response.Body.Bytes(), []byte("private-fixture")) {
		t.Fatal("unsafe status")
	}
	response = subscriptionCall(t, http.MethodPut, body, "https://gemini.example", true)
	if response.Code != http.StatusConflict {
		t.Fatalf("duplicate job accepted: %d", response.Code)
	}
}

func TestSubscriptionRejectsInvalidAndUnauthorizedMutations(t *testing.T) {
	dir := subscriptionTest(t)
	for _, tc := range []struct {
		body, origin string
		auth         bool
		want         int
	}{
		{`{"url":"https://example.com/sub"}`, "https://gemini.example", false, 401},
		{`{"url":"https://example.com/sub"}`, "https://attacker.example", true, 403},
		{`{"url":"http://example.com/sub"}`, "", true, 400},
		{`{"url":"https://127.0.0.1/sub"}`, "", true, 400},
		{`{"url":"https://user:pass@example.com/sub"}`, "", true, 400},
		{`{"url":"https://example.com/sub","command":"id"}`, "", true, 400},
		{`{"url":"https://example.com/sub"} trailing`, "", true, 400},
	} {
		response := subscriptionCall(t, http.MethodPut, tc.body, tc.origin, tc.auth)
		if response.Code != tc.want {
			t.Fatalf("got=%d want=%d", response.Code, tc.want)
		}
		if _, err := os.Stat(filepath.Join(dir, "proxy-subscription-request.json")); !os.IsNotExist(err) {
			t.Fatal("invalid request queued")
		}
	}
}

func TestSubscriptionRefreshAllowsBlankURLAndReportsInterruptedJob(t *testing.T) {
	dir := subscriptionTest(t)
	response := subscriptionCall(t, http.MethodPut, `{"url":"","restore_deleted":true}`, "", true)
	if response.Code != http.StatusAccepted {
		t.Fatalf("refresh rejected: %d", response.Code)
	}
	status := map[string]any{"available": true, "configured": true, "state": "syncing", "updated_at": time.Now().Unix() - 4000}
	data, _ := json.Marshal(status)
	if err := os.WriteFile(filepath.Join(dir, "proxy-subscription-status.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	response = subscriptionCall(t, http.MethodGet, "", "", true)
	if !bytes.Contains(response.Body.Bytes(), []byte(`"state":"failed"`)) {
		t.Fatal("stale job looks active forever")
	}
}

func TestSubscriptionRestoresOnlySelectedDeletionPreferences(t *testing.T) {
	resetSeedState(t)
	first, err := proxyCreate("to-mihomo:restore-one", "http://127.0.0.1:19001", 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := proxyCreate("to-mihomo:restore-two", "http://127.0.0.1:19002", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = proxyDeleteMany([]int64{first, second}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = getDB().Exec("DELETE FROM kv WHERE k IN (?,?)", deletedManagedProxyKey("to-mihomo:restore-one"), deletedManagedProxyKey("to-mihomo:restore-two"))
	})
	if _, err = restoreManagedProxyNames([]string{"not-a-managed-node"}); err == nil {
		t.Fatal("invalid name accepted")
	}
	count, err := restoreManagedProxyNames([]string{"to-mihomo:restore-one", "to-mihomo:restore-one"})
	if err != nil || count != 1 {
		t.Fatalf("restoration count=%d error=%v", count, err)
	}
	if kvGet(deletedManagedProxyKey("to-mihomo:restore-two")) == "" {
		t.Fatal("unselected deletion preference lost")
	}
	id, err := proxyCreate("to-mihomo:restore-one", "http://127.0.0.1:19001", 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = proxyDelete(id)
		_, _ = getDB().Exec("DELETE FROM kv WHERE k=?", deletedManagedProxyKey("to-mihomo:restore-one"))
	})
}
