package app

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestRotationChecksLoginThroughSelectedProxy(t *testing.T) {
	for _, valid := range []bool{false, true} {
		t.Run(map[bool]string{false: "unrecognized", true: "verified"}[valid], func(t *testing.T) {
			resetSeedState(t)
			proxyURL := "http://rotation-fixture.invalid:8080"
			pid, err := proxyCreate("rotation-fixture", proxyURL, 1)
			if err != nil {
				t.Fatal(err)
			}
			id, err := accountAdd("rotation", "SAPISID=dummy; __Secure-1PSID=dummy; __Secure-1PSIDTS=old", "")
			if err != nil {
				t.Fatal(err)
			}
			calls := []string{}
			stdlibClientCache.Store(proxyURL, &http.Client{Transport: cookieCheckTransport(func(r *http.Request) (*http.Response, error) {
				calls = append(calls, r.URL.Path)
				h := make(http.Header)
				body := "ok"
				switch r.URL.Path {
				case "/RotateCookiesPage":
					body = "init('fixtureid', 658.0, 0.0, 0.0, 600.0)"
				case "/RotateCookies":
					h.Add("Set-Cookie", "SIDCC=updated; Path=/")
				case "/app":
					body = "<html>unknown</html>"
					if valid {
						body = `{"SNlM0e":"fixture-token-123456"}`
					}
				}
				return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})})
			t.Cleanup(func() { stdlibClientCache.Delete(proxyURL) })
			_, _, err = rotateAccount(*accountByID(id))
			a := accountByID(id)
			if (err == nil) != valid {
				t.Fatalf("valid=%v err=%v", valid, err)
			}
			if a.ProxyID != pid {
				t.Fatalf("proxy binding = %d, want %d", a.ProxyID, pid)
			}
			if len(calls) == 0 || calls[len(calls)-1] != "/app" {
				t.Fatalf("login not checked through proxy: %v", calls)
			}
			if valid && a.LastOkAt == 0 {
				t.Fatal("successful verification not saved")
			}
			if !valid && (a.FailCount != 0 || a.Status != "enabled" || cookieAcctView(*a)["health"] != "pending") {
				t.Fatal("unverified login mislabeled")
			}
		})
	}
}
