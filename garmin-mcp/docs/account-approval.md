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

**At the end of the browser login** — `internal/loginweb/remoteflow.go`, in
`recordPrivacyConsent`, immediately after the privacy acceptance is stored and before
anything is granted.

That order is deliberate. A held account reaches the consent page like any other, with
a banner saying it is waiting, and it **can accept the privacy notice**: accepting is
the person's decision about their own data, and making it wait on the operator's
decision about their access would leave someone able to read the notice but not to
consent to it. So the acceptance is recorded, and then the gate refuses the grant: the
person gets the `pending` page, the OAuth transaction is closed rather than left to
expire, and no code or token is issued. When the operator approves, the person comes
back through their client and is not asked for the notice again.

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

## Telling the operator

Nothing announces a held account by itself, so an operator who does not watch the web
interface learns about it when the person complains. Configure an SMTP server and the
deployment sends one e-mail per held account instead.

| Setting | Meaning |
| ------- | ------- |
| `smtp-host` | the server. **Empty sends no mail**, which is the default and a complete configuration. |
| `smtp-port` | `587` for STARTTLS (the default), `465` for implicit TLS. |
| `smtp-user` | the account to authenticate as. With Gmail, the full address. |
| `smtp-secret-file` | an **owner-only file** holding the secret. With Gmail, an application secret. |
| `smtp-from` | the sender. Empty uses `smtp-user`, which Gmail requires anyway. |
| `smtp-to` | who is told. At least one address once a host is set. |
| `smtp-tls` | `starttls` or `implicit`. There is no cleartext mode. |
| `dashboard-url` | a link to the web interface, put in the message. |

The secret is deliberately **not** a setting of its own. `internal/config` refuses any
key whose name carries a credential — a test enforces it — and this fork keeps that
rule rather than working around it: only the path of a file is configured, exactly as
for the master key, and `internal/securefile` refuses to read it if any other local
account can.

Three properties are worth knowing:

- **A send never delays or fails a login.** `internal/cmd/heldaccounts.go` returns
  immediately and does the work in a goroutine, on a context detached from the request
  — the request's own context is cancelled the moment the page is written.
- **One account is announced once per interval** (six hours by default). Someone who
  retries a login three times produces one e-mail, and someone who comes back a week
  later produces a reminder. A send that *fails* does not consume the interval.
- **A half-filled configuration refuses to start.** Naming a server and forgetting the
  recipients is a mistake an operator would only discover by not receiving anything.

The message carries the account's e-mail address, its internal identifier and the
instant — no Garmin data. The privacy notice says so, because that address then
transits through a third party the operator chose.

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
