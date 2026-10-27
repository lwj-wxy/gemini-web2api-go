package app

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
)

const maxProxyDeleteIDs = 1000
const deletedManagedProxyPrefix = "deleted-managed-proxy/"

var errInvalidProxySelection = errors.New("select between 1 and 1000 positive proxy IDs")
var errManagedProxyDeleted = errors.New("this subscription node was explicitly deleted")
var proxyMutationMu sync.Mutex
var proxyReloadMu sync.Mutex

func deletedManagedProxyKey(name string) string {
	digest := sha256.Sum256([]byte(name))
	return deletedManagedProxyPrefix + hex.EncodeToString(digest[:])
}

func excludedManagedProxyNames() ([]string, error) {
	rows, err := getDB().Query("SELECT v FROM kv WHERE k LIKE ? ORDER BY v", deletedManagedProxyPrefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// A manual subscription import may restore only the names present in its
// validated subscription, without clearing unrelated deletion preferences.
func restoreManagedProxyNames(names []string) (int64, error) {
	if len(names) == 0 || len(names) > 512 {
		return 0, errInvalidProxySelection
	}
	args := []any{}
	seen := map[string]bool{}
	for _, name := range names {
		if !strings.HasPrefix(name, "to-mihomo:") || len(name) > 100 {
			return 0, errInvalidProxySelection
		}
		key := deletedManagedProxyKey(name)
		if !seen[key] {
			seen[key] = true
			args = append(args, key)
		}
	}
	proxyMutationMu.Lock()
	defer proxyMutationMu.Unlock()
	where := "(" + strings.TrimSuffix(strings.Repeat("?,", len(args)), ",") + ")"
	result, err := getDB().Exec("DELETE FROM kv WHERE k IN "+where, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// Clear bindings, persist deliberate managed-node removals and delete rows in
// one transaction. Request history and all other proxy/account data are kept.
func proxyDeleteMany(ids []int64) (int64, error) {
	if len(ids) == 0 || len(ids) > maxProxyDeleteIDs {
		return 0, errInvalidProxySelection
	}
	selected := make(map[int64]bool, len(ids))
	args := []any{}
	for _, id := range ids {
		if id <= 0 {
			return 0, errInvalidProxySelection
		}
		if !selected[id] {
			selected[id] = true
			args = append(args, id)
		}
	}
	proxyMutationMu.Lock()
	defer proxyMutationMu.Unlock()
	// A scheduled reload must not publish a pre-deletion snapshot afterwards.
	proxyReloadMu.Lock()
	defer proxyReloadMu.Unlock()
	tx, err := getDB().Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	where := "(" + strings.TrimSuffix(strings.Repeat("?,", len(args)), ",") + ")"
	rows, err := tx.Query("SELECT name FROM proxies WHERE id IN "+where, args...)
	if err != nil {
		return 0, err
	}
	names := []string{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			return 0, err
		}
		if strings.HasPrefix(name, "to-mihomo:") {
			names = append(names, name)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	for _, name := range names {
		if _, err = tx.Exec(upsert("kv", []string{"k", "v"}, []string{"k"}), deletedManagedProxyKey(name), name); err != nil {
			return 0, err
		}
	}
	if _, err = tx.Exec("UPDATE accounts SET proxy_id=0 WHERE proxy_id IN "+where, args...); err != nil {
		return 0, err
	}
	result, err := tx.Exec("DELETE FROM proxies WHERE id IN "+where, args...)
	if err != nil {
		return 0, err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	proxyMu.Lock()
	kept := proxyCache[:0]
	for _, p := range proxyCache {
		if !selected[p.ID] {
			kept = append(kept, p)
		}
	}
	proxyCache = kept
	proxyMu.Unlock()
	return deleted, nil
}
