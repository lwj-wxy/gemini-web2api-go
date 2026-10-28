package app

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
)

// Only explicit authentication evidence may penalize or disable an account.
// A failed connection or an unrecognized page cannot establish login validity.
type cookieCheckError struct {
	code   string
	detail string
}

func (e *cookieCheckError) Error() string { return "[cookie-" + e.code + "] " + e.detail }

func cookieFailureCode(err error) string {
	var check *cookieCheckError
	if errors.As(err, &check) {
		return check.code
	}
	msg := err.Error()
	for _, code := range []string{"auth", "network", "blocked", "rate_limited", "unverified", "exit_unavailable"} {
		if strings.HasPrefix(msg, "[cookie-"+code+"] ") {
			return code
		}
	}
	// Compatibility with errors stored by earlier releases. Bare 302/403 and a
	// missing page token were previously overclassified; retain them as pending.
	if strings.Contains(msg, "fetch /app: HTTP 401") {
		return "auth"
	}
	if strings.Contains(msg, "HTTP 429") {
		return "rate_limited"
	}
	if strings.Contains(msg, "HTTP 403") || strings.Contains(msg, "sorry") {
		return "blocked"
	}
	var ne net.Error
	var ue *url.Error
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &ne) || errors.As(err, &ue) {
		return "network"
	}
	for _, text := range []string{"EOF", "timeout", "TLS handshake", "connection reset", "connection refused", "no such host"} {
		if strings.Contains(msg, text) {
			return "network"
		}
	}
	return "unverified"
}

func appPageStatusError(status int, location string) error {
	code := "unverified"
	switch status {
	case 401:
		code = "auth"
	case 403:
		code = "blocked"
	case 429:
		code = "rate_limited"
	case 301, 302, 303, 307, 308:
		u, err := url.Parse(location)
		if err == nil && strings.EqualFold(u.Hostname(), "accounts.google.com") &&
			(strings.HasPrefix(u.Path, "/ServiceLogin") || strings.HasPrefix(u.Path, "/v3/signin/") || strings.HasPrefix(u.Path, "/signin/")) {
			code = "auth"
		}
	}
	return &cookieCheckError{code: code, detail: fmt.Sprintf("fetch /app: HTTP %d", status)}
}

func recordCookieCheckError(id int64, err error) {
	if id <= 0 || err == nil {
		return
	}
	code := cookieFailureCode(err)
	message := err.Error()
	if !strings.HasPrefix(message, "[cookie-") {
		message = (&cookieCheckError{code: code, detail: message}).Error()
	}
	if code == "auth" {
		markAccountResult(id, false, message)
		return
	}
	// End the consecutive-auth-failure streak without declaring a success or
	// re-enabling a disabled account. LastOkAt and credentials are unchanged.
	_, _ = getDB().Exec(`UPDATE accounts SET fail_count=0, last_error=? WHERE id=?`, truncate(message, 200), id)
}

func cookieAccountHealth(a CookieAccount) (string, string) {
	if a.LastError != "" {
		code := cookieFailureCode(errors.New(a.LastError))
		if code == "auth" {
			return "dead", code
		}
		if a.Status != "enabled" {
			return "disabled", code
		}
		if a.LastOkAt > 0 && (code == "network" || code == "blocked" || code == "rate_limited" || code == "exit_unavailable") {
			return "ok", code
		}
		return "pending", code
	}
	// Older rows may have an auth counter but no diagnostic message.
	if a.FailCount > 0 {
		return "dead", "auth"
	}
	if a.Status != "enabled" {
		return "disabled", ""
	}
	if a.LastOkAt == 0 {
		return "unknown", ""
	}
	return "ok", ""
}

func recordCookiePrecheckError(id, proxyID int64, err error) {
	recordCookieCheckError(id, err)
	switch cookieFailureCode(err) {
	case "network", "blocked", "rate_limited":
		if proxyID > 0 {
			recordProxyResult(proxyID, false, "login precheck: "+err.Error())
		}
	}
}
