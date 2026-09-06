-- 0003_privacy_notice_consent.sql — the record that a person was shown the privacy
-- notice and accepted it, before any account of theirs was granted to a client.
--
-- The conventions of 0001 still hold: timestamps are TEXT holding RFC 3339 in UTC,
-- identifiers are TEXT, and nothing here is a credential. This table holds no new
-- category of data: a principal id, the digest of a document this build served, the
-- label that document carried, and an instant.
--
-- Why the digest is part of the key, and not only the label:
--
--   * The label is written by whoever edits the notice. It can be forgotten. The
--     digest cannot: it is computed from the exact bytes the person was shown, so a
--     row proves which text was accepted rather than which name it went under.
--   * A changed notice therefore finds no row and asks again, which is the correct
--     behaviour and needs no operator action to trigger.
--   * Two rows for one principal are the normal state of an account that has seen
--     two versions. Nothing is overwritten, so the trail of what was accepted, and
--     when, survives every later acceptance.
--
-- Withdrawal is deliberately not a column here, because a flag would not withdraw
-- anything. What stops the processing is `garmin-mcp revoke`, which ends the
-- authorizations, and `garmin-mcp unlink`, which drops the Garmin linkage and its
-- tokens. Both leave this row standing, and that is the intent: the row is the
-- evidence of which text the person was shown, and it should not disappear at the
-- moment it becomes relevant. It goes when the principal goes, by the cascade below.
CREATE TABLE privacy_notice_consents (
    principal_id   TEXT NOT NULL REFERENCES principals (id) ON DELETE CASCADE,
    -- Lowercase hex SHA-256 of the exact notice text that was rendered.
    notice_hash    TEXT NOT NULL,
    -- The human label the build carried for that text, for a report to read.
    notice_version TEXT NOT NULL,
    accepted_at    TEXT NOT NULL,
    PRIMARY KEY (principal_id, notice_hash)
) STRICT;

-- The lookup the login flow makes on every consent page: "has this principal
-- accepted anything, and what was the most recent".
CREATE INDEX idx_privacy_notice_consents_principal
    ON privacy_notice_consents (principal_id, accepted_at);
