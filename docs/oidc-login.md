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

OIDC sessions expire after at most ten minutes, or sooner when the ID/access
token expires. Tokens are discarded after sign-in. Reauthentication checks the
provider's current authorization; no refresh token extends a revoked grant.
Browser collaboration and event streams close at the same deadline; subsequent
authenticated requests must sign in again.

Signing out deletes the local session first, then takes the browser to the
provider's discovered end-session endpoint when available. Provider logout may
require confirmation. Other application sessions remain subject to their own
expiry/revocation policies.

Password sign-in and existing API tokens remain independent recovery/access
paths. Removing an OIDC entitlement does not revoke these credentials. For full
offboarding, also deactivate the native Salt account and revoke its tokens.
OIDC uses the provider's MFA policy; local password TOTP does not run on this
route.

To roll back, remove `SALT_OIDC_ISSUER` and restart, or return to the previous
binary. Existing content, accounts and memberships are unchanged. The additive
`sessions.auth_provider` column is compatible with the previous binary. Any
already-issued OIDC session is denied when the connector is disabled.
