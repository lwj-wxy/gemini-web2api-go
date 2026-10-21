# Cookie pool bulk login assistant

Scope: add a Cookie pool button and an authenticated, same-origin import panel.
The existing manual Cookie import and image-generation routes remain intact.

The assistant queues email/password/TOTP-secret records, creates one isolated
headed Chromium session at a time, generates TOTP locally, then collects only
Cookies applicable to Gemini. Unrecognized or additional security challenges
pause automation and expose an authenticated screenshot-and-input takeover
panel. CAPTCHA, SMS and device approvals are handled by the administrator.
Automation cannot guarantee that Google accepts every account or browser.

Pending credentials are encrypted at rest. Terminal/cancelled/interrupted tasks
erase their encrypted credential payloads. Browser profiles are ephemeral;
screenshots and entered verification text are never persisted. Responses and
logs contain no passwords, TOTP seeds or Cookie values. The sidecar has no
published host port; the Go admin server authenticates and proxies requests.
Mutations require a same-origin check and a custom request header.

An explicit direct connection or one fixed existing proxy is selected per task.
Automatic proxy allocation assigns enabled nodes once; it never silently falls
back to another node. A new Go endpoint verifies collected Cookies using that
exact egress before atomically inserting an enabled account with its binding.
Repeated completion is idempotent and existing accounts are not overwritten.

Validation covers RFC TOTP vectors, parsing, encryption and credential erasure,
queue transitions, interrupted work, cancellation, challenge takeover, exact
proxy reuse, authenticated APIs, verified-only insertion and real browser flows
against isolated local fixtures. Production smoke checks use no user accounts.
Real Google account acceptance requires the administrator's first import;
no credential sample has been supplied during development.

Deployment: retain original compose, native source/UI, database and environment;
build and test sidecar/native images before cutover; keep the current image
compatibility service unchanged. Use a canary with an isolated database for
admin/UI validation, then publish with rollback and verify account IDs/config.
