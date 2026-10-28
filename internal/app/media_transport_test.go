package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type partialMediaReader struct {
	sent bool
	data string
}

func (r *partialMediaReader) Read(p []byte) (int, error) {
	if r.sent {
		return 0, io.ErrUnexpectedEOF
	}
	r.sent = true
	data := r.data
	if data == "" {
		data = "partial response with conversation metadata\n"
	}
	return copy(p, []byte(data)), nil
}

func TestInterruptedImageIsRecoveredWithoutResubmission(t *testing.T) {
	for _, mode := range []string{"partial_image", "partial_conversation", "unknown_outcome"} {
		t.Run(mode, func(t *testing.T) {
			resetSeedState(t)
			old := rtCfg()
			rtMu.Lock()
			rtVal.GeminiBLAuto = false
			rtVal.AnonFirst = false
			rtVal.RetryAttempts = 3
			rtMu.Unlock()
			t.Cleanup(func() { rtMu.Lock(); rtVal = old; rtMu.Unlock() })
			proxyURL := "http://media-recovery-" + mode + ".invalid"
			pid, err := proxyCreate("fixture", proxyURL, 1)
			if err != nil {
				t.Fatal(err)
			}
			id, err := accountAdd("fixture", "SAPISID="+mode+"; __Secure-1PSID=id", "")
			if err != nil {
				t.Fatal(err)
			}
			bindAccountProxy(id, pid)
			frame := func(withImage bool) string {
				inner := []any{nil, []any{"c_fixture"}}
				if withImage {
					inner = append(inner, "https://lh3.googleusercontent.com/gg/fixture")
				}
				b, _ := json.Marshal(inner)
				outer, _ := json.Marshal([]any{[]any{"wrb.fr", "fixture", string(b)}})
				return string(outer) + "\n"
			}
			posts, history := 0, 0
			stdlibClientCache.Store(proxyURL, &http.Client{Transport: cookieCheckTransport(func(r *http.Request) (*http.Response, error) {
				head := make(http.Header)
				body := `{"SNlM0e":"fixture-token-123456"}`
				if strings.Contains(r.URL.Path, "StreamGenerate") {
					posts++
					if mode == "unknown_outcome" {
						return nil, context.DeadlineExceeded
					}
					return &http.Response{StatusCode: 200, Header: head, Body: io.NopCloser(&partialMediaReader{data: frame(mode == "partial_image")}), Request: r}, nil
				}
				if strings.Contains(r.URL.Path, "batchexecute") {
					history++
					body = frame(true)
				}
				if strings.Contains(r.URL.Path, "/gg/") {
					head.Set("Content-Type", "image/png")
					body = "fixture-image-bytes"
				}
				return &http.Response{StatusCode: 200, Header: head, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})})
			t.Cleanup(func() { stdlibClientCache.Delete(proxyURL) })
			result, err := streamGenerate("private fixture", "", ModelConfig{Tool: toolImage}, nil, nil)
			if posts != 1 {
				t.Fatalf("generation submitted %d times", posts)
			}
			if mode == "unknown_outcome" {
				if err == nil || !strings.Contains(err.Error(), "not resubmitted") {
					t.Fatalf("unknown outcome hidden: %v", err)
				}
				return
			}
			if err != nil || result == nil || len(result.Artifacts) != 1 {
				t.Fatalf("failed recovery: %v", err)
			}
			if mode == "partial_conversation" && history != 1 {
				t.Fatalf("history calls %d", history)
			}
		})
	}
}

func TestImageRecoveryHonorsSharedDeadline(t *testing.T) {
	u := "http://bounded-recovery.invalid"
	stdlibClientCache.Store(u, &http.Client{Transport: cookieCheckTransport(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })})
	t.Cleanup(func() { stdlibClientCache.Delete(u) })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := fetchImageArtifacts("", "c_fixture", "SAPISID=x", "x", "x", u, "image/png", ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline lost: %v", err)
	}
}
func TestMediaTransportRetainsPartialResponse(t *testing.T) {
	u := "http://partial-response.invalid"
	stdlibClientCache.Store(u, &http.Client{Transport: cookieCheckTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(&partialMediaReader{}), Request: r}, nil
	})})
	t.Cleanup(func() { stdlibClientCache.Delete(u) })
	_, raw, _, _, err := doGeminiRequest("https://gemini.google.com/fixture", "", nil, u, nil)
	if !errors.Is(err, io.ErrUnexpectedEOF) || len(raw) == 0 {
		t.Fatalf("lost recoverable response: bytes=%d err=%v", len(raw), err)
	}
}
