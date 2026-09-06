-- 0004_account_approval.sql — the operator's decision about whether an account may
-- use this deployment at all.
--
-- This is a fork addition. Upstream, an account exists as soon as a Garmin login
-- succeeds and may be used from that instant. Here the operator can require that a
-- new account waits for a decision, so a stranger who signs in gets an account that
-- holds nothing back but can do nothing yet.
--
-- The conventions of 0001 still hold: timestamps are TEXT holding RFC 3339 in UTC,
-- identifiers are TEXT, and nothing here is a credential.
--
-- Absence of a row is "pending", and that is the whole reason the table has no
-- pending state of its own: a principal created after this migration has no row, so
-- it is pending by construction. Nothing has to run, and no default can be forgotten,
-- for a new account to start out waiting.
CREATE TABLE account_approvals (
    principal_id TEXT PRIMARY KEY REFERENCES principals (id) ON DELETE CASCADE,
    -- approved lets the account through; blocked is a decision to refuse it, which
    -- reads differently from "nobody has looked yet" in a report and in the UI.
    state        TEXT NOT NULL CHECK (state IN ('approved', 'blocked')),
    decided_at   TEXT NOT NULL,
    -- Who decided, as the operator identity the deciding interface authenticated.
    -- It is display and audit data, never an authorization input.
    decided_by   TEXT NOT NULL DEFAULT '',
    -- An operator's note. Free text, bounded by the writer, never rendered as markup.
    note         TEXT NOT NULL DEFAULT ''
) STRICT;

-- Every account that already exists keeps working.
--
-- Without this, enabling the gate on a running deployment would lock out everyone at
-- once, which is not what "hold new accounts" means. The decision is recorded as made
-- by the migration itself, so a report can tell a grandfathered account from one an
-- operator actually looked at.
INSERT INTO account_approvals (principal_id, state, decided_at, decided_by, note)
SELECT id, 'approved', updated_at, 'migration', 'existed before approval was required'
  FROM principals;
