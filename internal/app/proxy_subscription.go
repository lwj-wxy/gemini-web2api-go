package app

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type proxySubscriptionStatus struct {
	Available  bool   `json:"available"`
	Configured bool   `json:"configured"`
	SourceHost string `json:"source_host,omitempty"`
	JobID      string `json:"job_id,omitempty"`
	State      string `json:"state"`
	Code       string `json:"code,omitempty"`
	NodeCount  int    `json:"node_count"`
	Checked    int    `json:"checked"`
	CheckTotal int    `json:"check_total"`
	Imported   int    `json:"imported"`
	Excluded   int    `json:"excluded"`
	Restored   int    `json:"restored"`
	UpdatedAt  int64  `json:"updated_at"`
}

var subscriptionQueueMu sync.Mutex

func proxySubscriptionDir() string {
	if directory := os.Getenv("GEMINI_PROXY_SUBSCRIPTION_DIR"); directory != "" {
		return directory
	}
	return filepath.Dir(cfg.DBPath)
}

func readProxySubscriptionStatus() proxySubscriptionStatus {
	result := proxySubscriptionStatus{State: "idle"}
	file, err := os.Open(filepath.Join(proxySubscriptionDir(), "proxy-subscription-status.json"))
	if err != nil {
		return result
	}
	defer file.Close()
	if json.NewDecoder(io.LimitReader(file, 65537)).Decode(&result) != nil {
		return proxySubscriptionStatus{State: "failed", Code: "status_unavailable"}
	}
	if subscriptionActive(result.State) && time.Now().Unix()-result.UpdatedAt > 90 {
		result.State = "failed"
		result.Code = "interrupted"
	}
	return result
}

func subscriptionActive(state string) bool {
	return state == "queued" || state == "waiting" || state == "validating" || state == "syncing"
}

func writeProxySubscriptionFile(name string, value any) error {
	directory := proxySubscriptionDir()
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".proxy-subscription-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err = json.NewEncoder(file).Encode(value); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(directory, name))
}

func proxySubscriptionOriginAllowed(r *http.Request) bool {
	if r.Header.Get("X-Gemini-Proxy") != "1" {
		return false
	}
	if raw := r.Header.Get("Origin"); raw != "" {
		origin, err := url.Parse(raw)
		scheme := r.URL.Scheme
		if scheme == "" {
			scheme = "http"
			if r.TLS != nil {
				scheme = "https"
			}
		}
		if forwarded := r.Header.Get("X-Forwarded-Proto"); forwarded == "http" || forwarded == "https" {
			scheme = forwarded
		}
		if err != nil || origin.Host != r.Host || origin.Scheme != scheme {
			return false
		}
	}
	return true
}

func validProxySubscriptionURL(raw string) bool {
	if len(raw) > 4096 || strings.ContainsAny(raw, "\r\n\x00") {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil && (!ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()) {
		return false
	}
	return true // The host worker validates and pins every resolved address too.
}

func handleProxySubscription(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		writeJSON(w, 200, readProxySubscriptionStatus())
		return
	}
	if r.Method != http.MethodPut {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	if !proxySubscriptionOriginAllowed(r) {
		writeJSON(w, 403, map[string]string{"error": "invalid request origin"})
		return
	}
	if cfg.AdminToken == "" {
		writeJSON(w, 503, map[string]string{"error": "administrator authentication required"})
		return
	}
	var request struct {
		URL            string `json:"url"`
		RestoreDeleted bool   `json:"restore_deleted"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid subscription request"})
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		writeJSON(w, 400, map[string]string{"error": "invalid subscription request"})
		return
	}
	request.URL = strings.TrimSpace(request.URL)
	if request.URL != "" && !validProxySubscriptionURL(request.URL) {
		writeJSON(w, 400, map[string]string{"error": "a public HTTPS subscription URL is required"})
		return
	}
	subscriptionQueueMu.Lock()
	defer subscriptionQueueMu.Unlock()
	status := readProxySubscriptionStatus()
	if !status.Available {
		writeJSON(w, 503, map[string]string{"error": "subscription service unavailable"})
		return
	}
	if subscriptionActive(status.State) {
		writeJSON(w, 409, status)
		return
	}
	if _, err := os.Lstat(filepath.Join(proxySubscriptionDir(), "proxy-subscription-request.json")); err == nil {
		writeJSON(w, 409, map[string]string{"error": "subscription job pending"})
		return
	}
	if request.URL == "" && !status.Configured {
		writeJSON(w, 400, map[string]string{"error": "subscription URL required"})
		return
	}
	jobID := newSessionToken()
	status.JobID = jobID
	status.State = "queued"
	status.Code = ""
	status.Checked = 0
	status.CheckTotal = 0
	status.Imported = 0
	status.Excluded = 0
	status.Restored = 0
	status.UpdatedAt = time.Now().Unix()
	if err := writeProxySubscriptionFile("proxy-subscription-status.json", status); err != nil {
		writeJSON(w, 500, map[string]string{"error": "could not queue subscription"})
		return
	}
	queued := map[string]any{"job_id": jobID, "url": request.URL, "restore_deleted": request.RestoreDeleted, "created_at": status.UpdatedAt}
	if err := writeProxySubscriptionFile("proxy-subscription-request.json", queued); err != nil {
		status.State = "failed"
		status.Code = "queue_failed"
		_ = writeProxySubscriptionFile("proxy-subscription-status.json", status)
		writeJSON(w, 500, map[string]string{"error": "could not queue subscription"})
		return
	}
	writeJSON(w, 202, status)
}

func handleProxyExclusions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	var request struct {
		Names []string `json:"names"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid restoration selection"})
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		writeJSON(w, 400, map[string]string{"error": "invalid restoration selection"})
		return
	}
	count, err := restoreManagedProxyNames(request.Names)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid restoration selection"})
		return
	}
	writeJSON(w, 200, map[string]any{"restored": count})
}
