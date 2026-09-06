# Holding a new account until an operator approves it

This is a fork addition, not part of the upstream project. Upstream, an account exists
and may be used from the instant a Garmin login succeeds. Here an operator can require
a decision first.

## The three states

| State | Stored as | Meaning |
| ----- | --------- | ------- |
| pending | no row in `account_approvals` | nobody has decided |
| approved | `state = 'approved'` | the account may be used |
| blocked | `state = 'blocked'` | the operator refused it |

Pending is deliberately not a stored value. An account created after migration `0004`
has no row, so it is pending by construction: nothing has to run, and no default can be
forgotten, for a new account to start out waiting. Both non-approved states refuse the
account; they differ in what they tell a human.

Migration `0004` approves every account that already existed, recorded as decided by
`migration`. Turning the gate on for a running deployment therefore locks nobody out.

## Where it is enforced, and why twice

**At the end of the browser login** — `internal/loginweb/remoteflow.go`,
`approvedAccount`. The check sits right after the Garmin login resolved a principal and
before anything is offered to grant, so a held account never reaches the consent page:
no privacy acceptance, no authorization code, no token. The person gets the `pending`
page, which carries the privacy notice, because their account exists and their data is
already stored whether or not the decision ever comes. The OAuth transaction is closed
rather than left to expire.

**On every access token.** The token select carries the approval state, and
`checkApproved` in `internal/store/sqlite_tokens.go` refuses a token whose account is
not approved, with `ErrAccountNotApproved`. This is what makes a withdrawn approval
bite at the account's next request instead of at its next sign-in, which is what an
operator who has just blocked someone expects.

Which reads apply it matters, because only one of them is on the path an MCP request
takes:

| Read | Gated | Why |
| ---- | ----- | --- |
| `ReadAccessToken` | yes | `oauthserver.VerifyAccessToken` authorizes every MCP request through it, by way of the `oauthstore` adapter. This is the one that stops a held account. |
| `RotateRefreshToken` | yes | it mints the next pair; a held account must not refresh its way to a working token. |
| `ReadRefreshToken` | no | it is not an authorization. The grant that consumes the token is gated above, and the RFC 7009 revocation endpoint reads it — revoking has to keep working for an account that was just blocked. |
| `LookupAccessToken` | yes | the resource-server read that judges expiry itself. Nothing in this build calls it today; it is gated for the caller that will. |

The error is distinct from `ErrTokenRevoked` on purpose: the token is intact, and
approving the account makes it work again.

## The setting

`require-account-approval` (`GARMIN_MCP_REQUIRE_ACCOUNT_APPROVAL`), default `true`.
False restores the upstream behaviour exactly: `SQLiteConfig.RequireApproval` is false,
so the token check does nothing, and `RemoteConfig.Approvals` is nil, so the login gate
does not exist. It is accepted but unused in stdio mode, which has one account and it
is the operator's own.

## Who writes the decision

The web interface in this repository, through `SetAccountApproval` in the store or,
equivalently, an `INSERT`/`DELETE` on `account_approvals`. There is no CLI subcommand
for it yet; `sqlite3` on the database works for an operator who wants one:

```sh
sqlite3 /data/garmin.db \
  "INSERT INTO account_approvals (principal_id, state, decided_at, decided_by, note)
   VALUES ('<principal-id>', 'approved', strftime('%Y-%m-%dT%H:%M:%SZ','now'), 'cli', '')
   ON CONFLICT (principal_id) DO UPDATE SET state = excluded.state;"
```

Removing the row puts the account back to pending.
