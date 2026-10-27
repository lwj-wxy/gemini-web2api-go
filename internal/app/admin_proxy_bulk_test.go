package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func proxyDeleteRequest(t *testing.T, body string, authorized bool) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodDelete, "/admin/api/proxies", bytes.NewBufferString(body))
	if authorized {
		request.Header.Set("Authorization", "Bearer "+cfg.AdminToken)
	}
	response := httptest.NewRecorder()
	requireAuth(handleAdminProxies)(response, request)
	return response
}

func TestAdminProxyBulkDelete(t *testing.T) {
	resetSeedState(t)
	t.Cleanup(func() { _, _ = getDB().Exec("DELETE FROM kv WHERE k LIKE ?", deletedManagedProxyPrefix+"%") })
	old := cfg.AdminToken
	cfg.AdminToken = "private-bulk-test"
	t.Cleanup(func() { cfg.AdminToken = old })
	first, _ := proxyCreate("first", "http://127.0.0.1:18081", 1)
	second, _ := proxyCreate("to-mihomo:removed", "http://127.0.0.1:18082", 1)
	keep, _ := proxyCreate("keep", "http://127.0.0.1:18083", 1)
	account, _ := accountAdd("private", "SAPISID=dummy; SID=x", "")
	_, err := getDB().Exec("UPDATE accounts SET proxy_id=? WHERE id=?", second, account)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"ids": []int64{first, second, second}})
	if got := proxyDeleteRequest(t, string(body), false); got.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", got.Code)
	}
	if len(listProxies()) != 3 {
		t.Fatal("unauthenticated request changed pool")
	}
	response := proxyDeleteRequest(t, string(body), true)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var reply struct {
		Deleted int `json:"deleted"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &reply); err != nil || reply.Deleted != 2 {
		t.Fatalf("reply=%s", response.Body.String())
	}
	rows := listProxies()
	if len(rows) != 1 || rows[0].ID != keep {
		t.Fatalf("unexpected remaining proxies: %v", rows)
	}
	var bound int64
	if err := getDB().QueryRow("SELECT proxy_id FROM accounts WHERE id=?", account).Scan(&bound); err != nil || bound != 0 {
		t.Fatalf("account binding=%d err=%v", bound, err)
	}
	list := httptest.NewRecorder()
	handleAdminProxies(list, httptest.NewRequest(http.MethodGet, "/admin/api/proxies", nil))
	var snapshot struct {
		Excluded []string `json:"excluded_managed_names"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, name := range snapshot.Excluded {
		found = found || name == "to-mihomo:removed"
	}
	if !found {
		t.Fatal("deleted managed node can be recreated by synchronization")
	}
	if _, err := proxyCreate("to-mihomo:removed", "http://127.0.0.1:18082", 1); err != errManagedProxyDeleted {
		t.Fatalf("stale sync can recreate removed node: %v", err)
	}
	// A repeated request reports zero and does not touch the remaining proxy.
	response = proxyDeleteRequest(t, string(body), true)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"deleted":0`)) {
		t.Fatalf("repeat deletion: %d %s", response.Code, response.Body.String())
	}
}

func TestAdminProxyBulkDeleteRejectsInvalidInput(t *testing.T) {
	resetSeedState(t)
	id, _ := proxyCreate("keep", "http://127.0.0.1:18081", 1)
	for _, body := range []string{`{}`, `{"ids":[]}`, `{"ids":[0]}`, `{"ids":[-1]}`, `{"ids":[1,"2"]}`, `{"ids":[1.5]}`, `{"ids":[1]} trailing`} {
		t.Run(body, func(t *testing.T) {
			response := proxyDeleteRequest(t, body, true)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			rows := listProxies()
			if len(rows) != 1 || rows[0].ID != id {
				t.Fatal("invalid request changed pool")
			}
		})
	}
	ids := make([]int64, maxProxyDeleteIDs+1)
	for i := range ids {
		ids[i] = id
	}
	body, _ := json.Marshal(map[string]any{"ids": ids})
	if response := proxyDeleteRequest(t, string(body), true); response.Code != http.StatusBadRequest {
		t.Fatalf("oversized selection accepted: %d", response.Code)
	}
}

func TestProxyBulkDeleteRollsBackBindingsAndExclusions(t *testing.T) {
	resetSeedState(t)
	first, _ := proxyCreate("to-mihomo:rollback", "http://127.0.0.1:18081", 1)
	keep, _ := proxyCreate("keep", "http://127.0.0.1:18082", 1)
	account, _ := accountAdd("private", "SAPISID=dummy; SID=x", "")
	if _, err := getDB().Exec("UPDATE accounts SET proxy_id=? WHERE id=?", first, account); err != nil {
		t.Fatal(err)
	}
	if _, err := getDB().Exec("INSERT INTO requests(ts,model,status,total_ms,prompt_chars,response_chars,prompt_tokens,output_tokens,proxy_id) VALUES(1,'private-history',200,1,0,0,0,0,?)", first); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = getDB().Exec("DELETE FROM requests WHERE model='private-history'") })
	_, err := getDB().Exec("CREATE TRIGGER proxy_bulk_failure BEFORE DELETE ON proxies BEGIN SELECT RAISE(ABORT,'private rollback test'); END")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = getDB().Exec("DROP TRIGGER IF EXISTS proxy_bulk_failure") })
	response := proxyDeleteRequest(t, `{"ids":[`+jsonNumber(first)+`]}`, true)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("failed deletion status=%d", response.Code)
	}
	if len(listProxies()) != 2 {
		t.Fatal("failed transaction changed cache")
	}
	var bound int64
	if err = getDB().QueryRow("SELECT proxy_id FROM accounts WHERE id=?", account).Scan(&bound); err != nil || bound != first {
		t.Fatalf("binding not rolled back: %d %v", bound, err)
	}
	if kvGet(deletedManagedProxyKey("to-mihomo:rollback")) != "" {
		t.Fatal("failed deletion persisted exclusion")
	}
	if _, err = getDB().Exec("DROP TRIGGER proxy_bulk_failure"); err != nil {
		t.Fatal(err)
	}
	if _, err = proxyDeleteMany([]int64{first}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = getDB().QueryRow("SELECT COUNT(*) FROM requests WHERE model='private-history' AND proxy_id=?", first).Scan(&count); err != nil || count != 1 {
		t.Fatal("history was removed")
	}
	rows := listProxies()
	if len(rows) != 1 || rows[0].ID != keep {
		t.Fatal("unselected proxy removed")
	}
	_, _ = getDB().Exec("DELETE FROM kv WHERE k=?", deletedManagedProxyKey("to-mihomo:rollback"))
}

func jsonNumber(id int64) string { value, _ := json.Marshal(id); return string(value) }
