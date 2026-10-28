package app

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestImageResultReportsHistoryRejection(t *testing.T) {
	proxyURL := "http://image-result.invalid:8080"
	calls := 0
	stdlibClientCache.Store(proxyURL, &http.Client{Transport: cookieCheckTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 403, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("blocked")), Request: r}, nil
	})})
	t.Cleanup(func() { stdlibClientCache.Delete(proxyURL) })
	_, err := fetchImageArtifacts("", "c_fixture", "SAPISID=dummy", "dummy", "fixture", proxyURL, "image/png")
	if err == nil || !strings.Contains(err.Error(), "hNvQHb HTTP 403") {
		t.Fatalf("history error hidden: %v", err)
	}
	if calls != 1 {
		t.Fatalf("permanent rejection queried %d times", calls)
	}
}

func TestImageResultMissingConversationFailsImmediately(t *testing.T) {
	_, err := fetchImageArtifacts("", "", "", "", "", "http://not-used.invalid", "image/png")
	if err == nil || !strings.Contains(err.Error(), "conversation") {
		t.Fatalf("missing conversation not reported: %v", err)
	}
}
