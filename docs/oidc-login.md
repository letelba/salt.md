# OpenID Connect login

Operators can add an OIDC provider alongside password, Google and Microsoft
sign-in. The connector uses Authorization Code flow, PKCE S256, one-time
browser-bound state, a nonce, and signature/issuer/audience/expiry validation
through `go-oidc`. It is disabled unless `SALT_OIDC_ISSUER` is configured.

## Configuration

Store secrets and account mappings in the deployment's secret store, never in
source or container build arguments. Restart after changing configuration.

| Environment variable | Purpose |
| --- | --- |
| `SALT_OIDC_ISSUER` | Exact discovery issuer; HTTPS required outside loopback development |
| `SALT_OIDC_CLIENT_ID` | Dedicated confidential client |
| `SALT_OIDC_CLIENT_SECRET` | Client secret |
| `SALT_OIDC_REDIRECT_URL` | Exact public URL ending in `/api/oidc/callback` |
| `SALT_OIDC_NAME` | Login button label; defaults to OpenID Connect |
| `SALT_OIDC_IDENTITY_CLAIM` | Immutable string claim used for account links; defaults to `sub` |
| `SALT_OIDC_ACCOUNT_LINKS` | Required JSON object mapping immutable identities to existing Salt user IDs |
| `SALT_OIDC_REQUIRED_ENTITLEMENT` | Optional exact value required in the `app_entitlements` array |
| `SALT_OIDC_REQUIRED_ROLE` | Optional exact value required in the `app_roles` array |
| `SALT_OIDC_REQUIRED_TEAM_ID` | Approved Slack workspace; requires matching `slack_team_id` and a nonempty `slack_user_id` when set |
| `SALT_OIDC_SESSION_KEYS` | Required comma-separated base64url 32-byte encryption keys; encrypt with the first, decrypt with any |

Register only the exact callback and public root URL as the post-logout URI.
Enable Authorization Code flow and require PKCE S256. Disable implicit flow and
password grants. The requested scopes are `openid profile email`.

For example, `SALT_OIDC_ACCOUNT_LINKS={"provider-subject":"existing-salt-user-id"}`
links exactly one approved identity. Obtain both IDs from authenticated provider
and Salt administration. Do not use email as the identity claim: email changes
and reassignment must not transfer access. There is no automatic email matching,
account creation, workspace membership sync, or role promotion. Salt continues
to enforce its native account-disabled flag and workspace permissions.

## Sessions, logout and recovery

OIDC sessions use the same **Sign-in session length (days)** instance setting as
password sign-ins (90 days by default). Snapshot that maximum at successful
login and cap it by `refresh_expires_in` when the provider exposes its remaining session lifetime.
Activity, renewal and later instance-setting changes never change that original
deadline. Only new sign-ins use the updated setting. A persistent opaque
HttpOnly, Secure, SameSite=Lax cookie contains no provider credentials.

SQLite stores the hashed handle, fixed expiry and authorization deadline, plus
an AES-GCM encrypted rotating refresh credential bound to the issuer, client,
identity and session. Keep `/data` persistent and retain the configured keys
across deployments. To rotate keys, prepend a new key and retain older keys until
all sessions encrypted with them have renewed or expired. Losing a required key
requires fresh login. Use the supplied `oidc-login.env.example` configuration
contract; configure secrets through your deployment's secret store.

Authorization lasts no more than 600 seconds or the signed access token's expiry,
whichever comes first. At the deadline, protected requests block for server-side
refresh and current UserInfo validation (subject, immutable identity, verified
email, workspace and required grants). A shared conditional database lease
serializes rotation across workers without holding a write transaction over HTTP.
The replacement credential is persisted before UserInfo/JWKS checks, allowing
recovery from temporary validation outages without retaining stale access.
Provider/store outages return 503; invalid credentials or grants remove the
session and return 401. No stale protected response is served.

Configure the provider's SSO idle/max to support the approved instance lifetime,
client overrides to inherit
(0), access tokens to 600 seconds, and rotating refresh credentials with zero
reuse. Do not request offline access. Existing SSO sessions retain their original
remaining lifetime; joining this app does not start a new provider session.

Browser collaboration and event streams close at the authorization deadline and
revalidate on reconnect. An authentication failure preserves the mounted editor,
pauses background requests and offers sign-in in another tab followed by an
explicit same-account retry. Failed HTTP mutations are never automatically
replayed. Temporary 503 failures retain drafts for deliberate retry.

Signing out deletes local session state and revokes its refresh credential before
taking the browser to the provider's end-session endpoint. Provider logout may
require confirmation. A concurrent renewal cannot recreate a logged-out session.

Password sign-in and existing API tokens remain independent recovery/access
paths. Removing an OIDC entitlement does not revoke these credentials. For full
offboarding, also deactivate the native Salt account and revoke its tokens.
OIDC uses the provider's MFA policy; local password TOTP does not run on this
route.

To disable the connector, remove `SALT_OIDC_ISSUER` and restart. Any OIDC
session is denied while disabled; native recovery still requires authentication.
Before returning to an older binary, disable the adapter and delete all sessions
with `auth_provider='oidc'` through private administration; older code does not
validate renewal metadata. Never restore a database snapshot just to undo this
adapter. Existing content, accounts and memberships are unchanged.

Tests in `server/oidc_sessions_test.go` cover injected-clock freshness and configured
fixed expiry boundaries, rotation, two workers sharing the database, a new server
instance reading persistent state, claim/revocation denial, outages, key rotation
and logout races. These simulations do not establish elapsed multi-day browser
acceptance; verify the deployed provider and browser independently.
