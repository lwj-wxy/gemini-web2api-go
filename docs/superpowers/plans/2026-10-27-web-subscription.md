# Direct subscription setup in the Gemini admin

Replace the command-only subscription dialog with a URL form, Save and synchronize, optional reimport of previously deleted nodes, and persisted progress/results. Blank URL refreshes the configured subscription. Saved URLs are never returned to the browser or written to logs.

Architecture: the existing authenticated Gemini service writes a bounded request file inside its existing data mount. A purpose-specific systemd path unit invokes a one-shot host worker; it accepts only subscription refresh/configuration fields, validates and downloads public HTTPS subscriptions, uses the existing synchronization lock and manager, and publishes non-secret status. No host shell, Docker socket or privileged command interface is exposed to the web container. Existing Grok direct policy and other service routing settings are preserved.

Validation: reject unauthenticated/cross-origin/invalid/oversized requests; test queue persistence, duplicate submission and stale jobs; verify invalid URLs cannot replace the working subscription; validate redirects/DNS targets and enforce size/timeout limits; test optional scoped restoration of deleted Gemini nodes. Run full Go tests, worker/synchronizer unit tests, isolated rendered UI flows and exact-commit server canary checks. Production UI verification must not change the subscription or restore proxy records.

Implementation steps:
- [ ] Native authenticated queue/status API and scoped managed-node restoration, with tests first.
- [ ] Bounded one-shot host worker and systemd path/service units, with network and lifecycle tests.
- [ ] Real URL form, progress, retry/results and reimport option, verified on desktop/mobile in both languages.
- [ ] Commit/push, build exact server commit, validate isolated canary and deploy the worker plus native image without version rollback.
- [ ] Verify production read-only flow and record evidence; preserve current subscription, proxy deletions and business data during QA.
