# Gemini proxy bulk deletion

Goal: let the administrator select proxies, select all filtered rows or disabled rows, confirm a count and delete in one operation. Existing proxy/cookie records must survive deployment; no production records are deleted during QA.

Design: add an authenticated, bounded DELETE /admin/api/proxies body with positive IDs. Deduplicate IDs and remove only those rows in a transaction, clearing their account bindings and preserving request history. Persist exclusions for deleted Mihomo node names so the external synchronizer does not recreate them. Single deletion uses the same operation. Add search, status filtering, accessible checkboxes, selected counts, confirmation, busy state and explicit errors to the existing admin screen. The subscription action shows the existing server configuration command without exposing the saved subscription URL.

Execution:
- [ ] Backend regression tests first: subset, duplicate IDs, invalid/empty/oversized input, authentication, transaction rollback, account/history preservation and managed-node exclusion.
- [ ] Implement transactional deletion and the controller's exclusion handling; run focused then full tests.
- [ ] Add UI controls; verify cancel, subset/all-filtered selection, errors, busy state and Chinese/English rendering using a temporary local database.
- [ ] Commit and push only relevant source. Build and test the exact committed source on the server, verify a canary, then update only the Gemini main-service image. Preserve cookie/import helper, compatibility service, configuration and all current records.
- [ ] Verify deployed assets/auth/empty-delete rejection and unchanged records; save private QA evidence.
