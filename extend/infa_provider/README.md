# Desktop managed inference v1

The managed provider uses `INFA_MANAGED_AUTH_FILE` plus a base64url JSON
`INFA_MANAGED_AUTH_SCOPE` locator. Both are required together. The CLI capability
probe is `xhermes --managed-inference-version` and prints `managed-inference-v1`.
Frozen manifests declare `managed_inference_protocol` with the same value.

Desktop Go owns the only user refresh chain. The private file contains a short
inference JWT, its expiration, a per-run proof key, fixed service URLs, and the
user / root session / billing context / run binding. It must not contain a user
refresh token, platform access token or long-lived device private key. The Go
writer replaces it atomically; the provider reloads and checks it at each HTTP
request boundary, including requests sent by long-lived SDK clients. Managed
sessions are read-only and never refresh themselves, write auth.json, follow a
new global login, or fall back to other CLI credentials.

The daemon pins `XHERMES_HOME` to the managed root/context. Missing, expired,
foreign or malformed credentials pause new calls. Removing the file prevents
subsequent calls; it does not replay or terminate an already admitted response.
The transport preserves the business Idempotency-Key across the nonce challenge
and uses only the pinned Gateway inference endpoints. Other OS processes running
as the same user can read that user's files; this is not process isolation.

The provider currently calls the existing v1 chat-completions/Responses APIs.
Those retain their personal legacy-plan/funding-project business boundary; the
new organization Coding Chat v2 text endpoint is separate. Organization Agent
tool-call billing requires its own integration acceptance. Standalone INFA CLI
login/refresh still uses the old Consumer contract and is not compatible with a
server after that contract is removed. The new Desktop managed path must be
released with matching Platform/Gateway/daemon/Desktop versions. Other providers
and unrelated standalone CLI configuration are not migrated.

Verification (canonical runner):

```bash
scripts/run_tests.sh tests/test_infa_provider.py tests/test_infa_managed_inference.py
```

These tests exercise the real provider module and HTTP transports with an
isolated credential file and simulated Gateway. They are not a live LLM,
multiplatform ACL, or frozen-bundle installation acceptance.
