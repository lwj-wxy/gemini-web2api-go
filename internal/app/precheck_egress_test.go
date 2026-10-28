package app

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestAlternatePrecheckOnlyUsesGETAndKeepsSuccessfulLease(t *testing.T) {
	resetSeedState(t)
	id, err := proxyCreate("alternative", "http://precheck-alternative.invalid", 1)
	if err != nil {
		t.Fatal(err)
	}
	proxyURL := proxyURLByID(id)
	stdlibClientCache.Store(proxyURL, &http.Client{Transport: cookieCheckTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.URL.Path != "/app" {
			t.Fatalf("unexpected generation request: %s %s", r.Method, r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"SNlM0e":"alternative-token-12345"}`)), Request: r}, nil
	})})
	t.Cleanup(func() { stdlibClientCache.Delete(proxyURL) })
	p, tok, ok := alternateCookiePrecheck("SAPISID=alternate", Proxy{ID: -100})
	if !ok || p.ID != id || tok != "alternative-token-12345" {
		t.Fatalf("alternate precheck failed: %v %v", ok, p.ID)
	}
	releaseSlot(p.ID)
}

func TestXSRFCacheSeparatesEgressAndInvalidatesAll(t *testing.T) {
	cookie := "SAPISID=cache-fixture"
	a, b := xsrfCacheKey(cookie, "http://one.invalid"), xsrfCacheKey(cookie, "http://two.invalid")
	if a == b {
		t.Fatal("different egress shares token")
	}
	xsrfMu.Lock()
	xsrfCache[a] = xsrfEntry{token: "a"}
	xsrfCache[b] = xsrfEntry{token: "b"}
	xsrfMu.Unlock()
	invalidateXSRF(cookie)
	xsrfMu.Lock()
	defer xsrfMu.Unlock()
	if _, ok := xsrfCache[a]; ok {
		t.Fatal("first cache survived")
	}
	if _, ok := xsrfCache[b]; ok {
		t.Fatal("second cache survived")
	}
}
