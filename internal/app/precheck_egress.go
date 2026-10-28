package app

import "time"

// Retry only the idempotent login-page GET, before any generation/upload. The
// caller owns a returned slot; failed attempts are always released here.
func alternateCookiePrecheck(cookie string, failed Proxy) (Proxy, string, bool) {
	attempts := 0
	for _, candidate := range listProxies() {
		if candidate.ID == failed.ID || !proxyUsable(candidate, time.Now().Unix(), rtCfg().ProxyCooldownMin) {
			continue
		}
		if acquired, _ := trySlotAcquire(candidate.ID); !acquired {
			continue
		}
		attempts++
		token, err := getXSRF(cookie, candidate.URL)
		if err == nil {
			logf("[proxy] 登录预检换出口恢复: %d -> %d", failed.ID, candidate.ID)
			return candidate, token, true
		}
		releaseSlot(candidate.ID)
		code := cookieFailureCode(err)
		if code == "network" || code == "blocked" || code == "rate_limited" {
			recordProxyResult(candidate.ID, false, "login precheck: "+err.Error())
		}
		if code != "network" || attempts >= 2 {
			break
		}
	}
	return Proxy{}, "", false
}
