# The privacy notice and its acceptance

This is a fork addition, not part of the upstream project. It puts a data-protection
notice in front of the consent decision and records what was accepted, in the same
database as everything else.

## What a person sees

The consent page — the last page of the browser login, the one with Autoriser and
Refuser —
carries a summary of what this deployment records and what it never records, always
visible, and the full text behind a disclosure they open if they want it. Below the
notice is an acceptance box. Granting without ticking it is refused by the server, not
only by the browser: the page comes back with the box still there and nothing granted.

Denying needs no acceptance. Refusing the notice and refusing the client are the same
act for someone who does not want this, and neither records anything.

Someone who has already accepted the exact text being served is not asked again. The
page tells them when they accepted it instead.

## What is recorded

One row per principal and per accepted text, in `privacy_notice_consents`:

| Column | Meaning |
| ------ | ------- |
| `principal_id` | the account, by its internal identifier |
| `notice_hash` | SHA-256 of the exact notice document that was shown |
| `notice_version` | the human label the build carried for that text |
| `accepted_at` | RFC 3339 UTC, the instant of the first acceptance |

Accepting the same text twice keeps the first instant: the first acceptance is the one
that happened. Accepting a second text adds a row rather than replacing one, so the
trail of what was accepted, and when, survives.

## Editing the text

The notice is `internal/loginweb/pages/remote/privacy.html`. It is a template with two
blocks — `privacy_summary` and `privacy_full` — and the consent page renders both. The
shipped text is in French, as are the labels this fork adds around it (the heading, the
disclosure, the acceptance box and the server's refusal message). The rest of the login
pages are upstream and remain in English.

Its bytes are digested at start-up, and an acceptance is recorded against that digest.
So editing anything in that file — a word, a heading, the whole text translated into
another language — makes every account see the notice again and accept it afresh. No
operator action triggers that; it follows from the file changing.

Bump `PrivacyNoticeVersion` in `internal/loginweb/privacy.go` when you edit the text,
so the stored row carries a label a human can read beside the digest a machine
compares.

The shipped text describes what this build actually does, checked against the schema
in `migrations/`. If you change what the server stores, the notice is part of the
change.

## Where it is enforced

`internal/loginweb/remoteflow.go`, in `recordPrivacyConsent`. The acceptance is written
**before** the authorization server is asked to grant anything, so a store that cannot
record it refuses the grant rather than issuing a token whose consent was never
persisted. A store that cannot be read serves 503 rather than guessing: "already
accepted" would let through someone who never consented, and "not accepted" would ask
for an acceptance that the same broken store could not keep.

`internal/loginweb/remoteprivacy_test.go` covers each of those paths.

## Withdrawal

There is deliberately no withdrawn flag. What stops the processing is
`garmin-mcp revoke`, which ends the authorizations, and `garmin-mcp unlink`, which
drops the Garmin linkage and its encrypted tokens. Both leave the acceptance row
standing — it is the evidence of which text the person was shown, and it should not
vanish at the moment it becomes relevant. It goes when the principal is deleted, by the
cascade in the migration.
