package app

import (
	"io"
	"testing"
)

func TestVerifiedCookieSurvivesNetworkFailure(t *testing.T) {
	resetSeedState(t)
	id, err := accountAdd("verified", "SAPISID=dummy; __Secure-1PSID=dummy", "")
	if err != nil {
		t.Fatal(err)
	}
	markAccountResult(id, true, "")
	before := accountByID(id).LastOkAt
	recordCookieCheckError(id, io.EOF)
	a := accountByID(id)
	if health, reason := cookieAccountHealth(*a); health != "ok" || reason != "network" {
		t.Fatalf("verified state lost: %s %s", health, reason)
	}
	if a.LastOkAt != before || a.FailCount != 0 {
		t.Fatal("network failure changed verification evidence")
	}
	markCookieByStatus(id, 401, "HTTP 401")
	if h, _ := cookieAccountHealth(*accountByID(id)); h != "dead" {
		t.Fatal("real auth rejection must still invalidate")
	}
}

func TestPrecheckNetworkErrorsTripProxyCircuit(t *testing.T) {
	resetSeedState(t)
	pid, err := proxyCreate("broken", "http://broken-precheck.invalid", 1)
	if err != nil {
		t.Fatal(err)
	}
	id, err := accountAdd("verified", "SAPISID=dummy; __Secure-1PSID=dummy", "")
	if err != nil {
		t.Fatal(err)
	}
	markAccountResult(id, true, "")
	for i := 0; i < proxyFailThreshold; i++ {
		recordCookiePrecheckError(id, pid, io.EOF)
	}
	if proxyUsableByID(pid) {
		t.Fatal("failed prechecks never opened proxy circuit")
	}
	if h, _ := cookieAccountHealth(*accountByID(id)); h != "ok" {
		t.Fatal("proxy failure invalidated verified account")
	}
}
