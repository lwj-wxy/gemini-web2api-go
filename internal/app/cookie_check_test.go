package app

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCookieCheckResponseEvidence(t *testing.T) {
	cases := []struct {
		name                  string
		status                int
		location, body, state string
		fails                 int64
	}{
		{"blocked", 403, "", "<html>challenge</html>", "pending", 0},
		{"limited", 429, "", "", "pending", 0},
		{"unknown_redirect", 302, "https://www.google.com/sorry/", "", "pending", 0},
		{"missing_token", 200, "", "<html>unrecognized page</html>", "pending", 0},
		{"login_redirect", 302, "https://accounts.google.com/ServiceLogin?continue=redacted", "", "invalid", 1},
		{"lookalike_redirect", 302, "https://accounts.google.com.example.invalid/ServiceLogin", "", "pending", 0},
		{"unauthorized", 401, "", "", "invalid", 1},
		{"valid", 200, "", `{"SNlM0e":"fixture-token-123456"}`, "valid", 0},
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetSeedState(t)
			proxyURL := fmt.Sprintf("http://cookie-evidence-%d.invalid:8080", index)
			pid, err := proxyCreate("fixture", proxyURL, 1)
			if err != nil {
				t.Fatal(err)
			}
			id, err := accountAdd("fixture", "SAPISID=dummy; __Secure-1PSID=dummy", "")
			if err != nil {
				t.Fatal(err)
			}
			bindAccountProxy(id, pid)
			stdlibClientCache.Store(proxyURL, &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: cookieCheckTransport(func(r *http.Request) (*http.Response, error) {
				h := make(http.Header)
				h.Set("Location", tc.location)
				return &http.Response{StatusCode: tc.status, Header: h, Body: io.NopCloser(strings.NewReader(tc.body)), Request: r}, nil
			})})
			t.Cleanup(func() { stdlibClientCache.Delete(proxyURL) })
			result := checkAccountCookie(*accountByID(id))
			if result.State != tc.state || result.OK != (tc.state == "valid") || result.ProxyName != "fixture" {
				t.Fatalf("unexpected result: %+v", result)
			}
			a := accountByID(id)
			if a.FailCount != tc.fails || a.Status != "enabled" || (a.LastOkAt != 0) != result.OK {
				t.Fatalf("invalid state mutation: fails=%d status=%s lastOK=%d", a.FailCount, a.Status, a.LastOkAt)
			}
		})
	}
}

func TestPendingCheckDoesNotEnableDisabledOrClaimSuccess(t *testing.T) {
	resetSeedState(t)
	id, err := accountAdd("disabled", "SAPISID=dummy; __Secure-1PSID=dummy", "")
	if err != nil {
		t.Fatal(err)
	}
	markCookieByStatus(id, 200, "")
	for i := 0; i < maxCookieAuthFailures; i++ {
		markCookieByStatus(id, 401, "HTTP 401")
	}
	before := accountByID(id)
	recordCookieCheckError(id, io.EOF)
	a := accountByID(id)
	if a.Status != "disabled" || a.LastOkAt != before.LastOkAt || a.Cookie != before.Cookie {
		t.Fatal("pending result changed disabled status, credentials or last successful check")
	}
}

type cookieCheckTransport func(*http.Request) (*http.Response, error)

func (f cookieCheckTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGenerationPrecheckStopsOnBrokenEgress(t *testing.T) {
	resetSeedState(t)
	previous := rtCfg()
	rtMu.Lock()
	rtVal.GeminiBLAuto = false
	rtVal.AnonFirst = false
	rtMu.Unlock()
	t.Cleanup(func() { rtMu.Lock(); rtVal = previous; rtMu.Unlock() })
	proxyURL := "http://generation-check.invalid:8080"
	pid, err := proxyCreate("fixture", proxyURL, 1)
	if err != nil {
		t.Fatal(err)
	}
	id, err := accountAdd("first", "SAPISID=first; __Secure-1PSID=first", "")
	if err != nil {
		t.Fatal(err)
	}
	id2, err := accountAdd("second", "SAPISID=second; __Secure-1PSID=second", "")
	if err != nil {
		t.Fatal(err)
	}
	bindAccountProxy(id, pid)
	calls := 0
	stdlibClientCache.Store(proxyURL, &http.Client{Transport: cookieCheckTransport(func(*http.Request) (*http.Response, error) { calls++; return nil, io.EOF })})
	t.Cleanup(func() { stdlibClientCache.Delete(proxyURL) })
	_, err = streamGenerate("private fixture", "", ModelConfig{}, nil, nil)
	if err == nil || calls != 1 {
		t.Fatalf("broken egress should stop before rotation/account hopping: calls=%d err=%v", calls, err)
	}
	if a := accountByID(id); a.FailCount != 0 || a.Status != "enabled" {
		t.Fatal("first account penalized")
	}
	if a := accountByID(id2); a.LastError != "" || a.LastUsedAt != 0 {
		t.Fatal("other account unnecessarily tried")
	}
}

func TestCookieCheckConnectionFailureIsNotExpired(t *testing.T) {
	resetSeedState(t)
	proxyURL := "http://cookie-check.invalid:8080"
	pid, err := proxyCreate("check-fixture", proxyURL, 1)
	if err != nil {
		t.Fatal(err)
	}
	id, err := accountAdd("check-fixture", "SAPISID=dummy; __Secure-1PSID=dummy", "")
	if err != nil {
		t.Fatal(err)
	}
	bindAccountProxy(id, pid)
	stdlibClientCache.Store(proxyURL, &http.Client{Transport: cookieCheckTransport(func(*http.Request) (*http.Response, error) { return nil, io.EOF })})
	t.Cleanup(func() { stdlibClientCache.Delete(proxyURL) })
	for i := 0; i < maxCookieAuthFailures+1; i++ {
		result := checkAccountCookie(*accountByID(id))
		if result.OK {
			t.Fatal("EOF must not verify login")
		}
	}
	a := accountByID(id)
	if a.FailCount != 0 || a.Status != "enabled" || a.LastOkAt != 0 {
		t.Fatalf("network errors poisoned account: fails=%d status=%s ok=%d", a.FailCount, a.Status, a.LastOkAt)
	}
	if got := cookieAcctView(*a)["health"]; got != "pending" {
		t.Fatalf("health=%v, want pending", got)
	}
}

func TestLegacyCookieCheckFailureView(t *testing.T) {
	for _, msg := range []string{`Get "https://gemini.google.com/app": EOF`, "fetch /app: HTTP 403", "fetch /app: HTTP 302", "no SNlM0e in page (cookie expired or not signed in)"} {
		a := CookieAccount{Status: "enabled", FailCount: 1, LastError: msg}
		if got := cookieAcctView(a)["health"]; got != "pending" {
			t.Errorf("%s: health=%v, want pending", msg, got)
		}
		if strings.Contains(explainCookieFailure(io.ErrUnexpectedEOF), "失效") {
			t.Fatal("EOF explained as expiration")
		}
	}
}

func TestForbiddenCookieResponseDoesNotDisable(t *testing.T) {
	resetSeedState(t)
	id, err := accountAdd("blocked", "SAPISID=dummy; __Secure-1PSID=dummy", "")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxCookieAuthFailures+1; i++ {
		markCookieByStatus(id, 403, "HTTP 403: blocked by upstream")
	}
	a := accountByID(id)
	if a.Status != "enabled" || a.FailCount != 0 {
		t.Fatalf("403 block disabled account: status=%s fails=%d", a.Status, a.FailCount)
	}
}
