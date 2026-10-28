package app

import "testing"

func TestCookieRefreshPreservesConcurrentChanges(t *testing.T) {
	resetSeedState(t)
	base := "SAPISID=sap; __Secure-1PSID=id; __Secure-1PSIDTS=old; SIDCC=old"
	id, err := accountAdd("fixture", base, "")
	if err != nil {
		t.Fatal(err)
	}
	first := "SAPISID=sap; __Secure-1PSID=id; __Secure-1PSIDTS=new; SIDCC=old"
	second := "SAPISID=sap; __Secure-1PSID=id; __Secure-1PSIDTS=old; SIDCC=new"
	updateAccountCookieFrom(id, base, first)
	updateAccountCookieFrom(id, base, second)
	a := accountByID(id)
	if cookieValue(a.Cookie, "__Secure-1PSIDTS") != "new" || cookieValue(a.Cookie, "SIDCC") != "new" {
		t.Fatal("concurrent refresh lost a newer cookie")
	}
	updateAccountCookieFrom(id, base, "SAPISID=sap; __Secure-1PSID=id; __Secure-1PSIDTS=stale; SIDCC=old")
	if cookieValue(accountByID(id).Cookie, "__Secure-1PSIDTS") != "new" {
		t.Fatal("stale response overwrote newer cookie")
	}
	updateAccountCookieFrom(id, base, "SAPISID=other; __Secure-1PSID=id; SIDCC=bad")
	if cookieValue(accountByID(id).Cookie, "SAPISID") != "sap" {
		t.Fatal("identity change accepted")
	}
}
