package app

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httputil"
	"net/mail"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

var importAccountMu sync.Mutex
var importJobID = regexp.MustCompile(`^[a-f0-9]{32}$`)
var verifyImportCookie = func(cookie, proxyURL string) error {
	invalidateXSRF(cookie)
	token, err := getXSRF(cookie, proxyURL)
	if err != nil || token == "" {
		return errors.New("登录态检测未通过，未加入账号池")
	}
	return nil
}

func assistantMutationAllowed(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	if r.Header.Get("X-Gemini-Import") != "1" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
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
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host != r.Host || u.Scheme != scheme {
			return false
		}
	}
	return true
}

// Only this authenticated proxy is exposed. The browser worker has no host port.
func handleImportAssistant(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !assistantMutationAllowed(r) {
		writeJSON(w, 403, map[string]string{"error": "请求来源不正确"})
		return
	}
	endpoint, key := os.Getenv("GEMINI_IMPORT_ASSISTANT_URL"), os.Getenv("GEMINI_IMPORT_ASSISTANT_KEY")
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.Scheme != "http" || key == "" || cfg.AdminToken == "" {
		writeJSON(w, 503, map[string]string{"error": "批量导入助手尚未配置"})
		return
	}
	path := "/"
	if strings.HasPrefix(r.URL.Path, "/admin/api/import-assistant/") {
		path = "/api/" + strings.TrimPrefix(r.URL.Path, "/admin/api/import-assistant/")
	} else if r.URL.Path != "/admin/import-assistant/" {
		writeJSON(w, 404, map[string]string{"error": "not found"})
		return
	}
	if strings.Contains(path, "..") || strings.Contains(path, "//") {
		writeJSON(w, 400, map[string]string{"error": "invalid path"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	proxy := httputil.NewSingleHostReverseProxy(u)
	director := proxy.Director
	proxy.Director = func(req *http.Request) {
		director(req)
		req.URL.Path, req.URL.RawPath, req.URL.RawQuery = path, "", ""
		req.Host = u.Host
		req.Header.Del("Authorization")
		req.Header.Del("Cookie")
		req.Header.Set("X-Import-Assistant-Key", key)
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		response.Header.Set("Cache-Control", "no-store")
		response.Header.Set("X-Frame-Options", "SAMEORIGIN")
		response.Header.Set("X-Content-Type-Options", "nosniff")
		response.Header.Del("Set-Cookie")
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		writeJSON(w, 503, map[string]string{"error": "导入助手暂不可用，请稍后重试"})
	}
	proxy.ServeHTTP(w, r)
}

// Verify before insertion and use the exact login proxy, with no slot fallback.
func handleVerifiedCookieImport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	key := os.Getenv("GEMINI_IMPORT_ASSISTANT_KEY")
	if key == "" || subtle.ConstantTimeCompare([]byte(key), []byte(r.Header.Get("X-Import-Assistant-Key"))) != 1 {
		writeJSON(w, 403, map[string]string{"error": "service authentication required"})
		return
	}
	if r.Method != http.MethodPost || !assistantMutationAllowed(r) {
		writeJSON(w, 403, map[string]string{"error": "invalid import request"})
		return
	}
	var body struct {
		Email     string `json:"email"`
		Cookie    string `json:"cookie"`
		JobID     string `json:"job_id"`
		ProxyID   int64  `json:"proxy_id"`
		ProxyHash string `json:"proxy_url_hash"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid JSON"})
		return
	}
	body.Email = strings.ToLower(strings.TrimSpace(body.Email))
	email, err := mail.ParseAddress(body.Email)
	if err != nil || email.Address != body.Email || len(body.Email) > 254 || !importJobID.MatchString(body.JobID) ||
		body.ProxyID < 0 || extractSAPISID(body.Cookie) == "" || cookieValue(body.Cookie, "__Secure-1PSID") == "" ||
		strings.ContainsAny(body.Cookie, "\r\n") {
		writeJSON(w, 400, map[string]string{"error": "账号或登录态不完整"})
		return
	}
	proxyURL := ""
	if body.ProxyID != 0 {
		found := false
		for _, p := range listProxies() {
			if p.ID == body.ProxyID && p.Enabled {
				proxyURL = p.URL
				found = true
				break
			}
		}
		if !found {
			writeJSON(w, 409, map[string]string{"error": "登录代理已停用，未入池"})
			return
		}
	}
	digest := sha256.Sum256([]byte(proxyURL))
	if body.ProxyHash != hex.EncodeToString(digest[:]) {
		writeJSON(w, 409, map[string]string{"error": "登录出口配置发生变化，未入池"})
		return
	}
	importAccountMu.Lock()
	defer importAccountMu.Unlock()
	note := "批量登录导入:" + body.JobID
	for _, account := range accountList() {
		if account.Note == note {
			writeJSON(w, 200, map[string]interface{}{"id": account.ID, "duplicate": false})
			return
		}
	}
	if err := verifyImportCookie(body.Cookie, proxyURL); err != nil {
		writeJSON(w, 422, map[string]string{"error": "登录态检测未通过，未加入账号池"})
		return
	}
	for _, account := range accountList() {
		if strings.EqualFold(account.Label, body.Email) || cookieValue(account.Cookie, "__Secure-1PSID") == cookieValue(body.Cookie, "__Secure-1PSID") {
			writeJSON(w, 200, map[string]interface{}{"id": account.ID, "duplicate": true})
			return
		}
	}
	if r.Context().Err() != nil {
		return
	}
	now := time.Now().Unix()
	id, err := insertID(`INSERT INTO accounts(label,cookie,status,note,created_at,last_ok_at,proxy_id) VALUES(?,?,?,?,?,?,?)`, body.Email, body.Cookie, "enabled", note, now, now, body.ProxyID)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "保存账号失败，请重试"})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"id": id, "duplicate": false})
}
