"""Fixtures : une base SQLite au schéma de garmin-mcp, peuplée de cas limites."""

from __future__ import annotations

import sqlite3
from datetime import UTC, datetime, timedelta
from pathlib import Path

import pytest

SCHEMA = Path(__file__).parent / "schema.sql"

# Instant de référence des tests : toutes les dates en dérivent.
NOW = datetime(2026, 9, 6, 12, 0, 0, tzinfo=UTC)


def iso(moment: datetime) -> str:
    """Format d'horodatage de garmin-mcp : RFC 3339, UTC, suffixe Z."""

    return moment.astimezone(UTC).strftime("%Y-%m-%dT%H:%M:%SZ")


def ago(**kwargs: float) -> str:
    return iso(NOW - timedelta(**kwargs))


@pytest.fixture
def now() -> datetime:
    return NOW


@pytest.fixture
def database_path(tmp_path: Path) -> Path:
    """Base peuplée de quatre comptes couvrant les quatre états dérivés."""

    path = tmp_path / "garmin.db"
    connection = sqlite3.connect(path)
    connection.executescript(SCHEMA.read_text(encoding="utf-8"))

    connection.execute(
        "INSERT INTO schema_migrations VALUES (1, 'initial', 'x', ?)", (ago(days=90),)
    )
    connection.execute(
        "INSERT INTO schema_migrations VALUES (2, 'oauth_contract', 'y', ?)", (ago(days=90),)
    )
    connection.executemany(
        "INSERT INTO oauth_clients (id, name, secret_hash, is_public, created_at, disabled_at)"
        " VALUES (?, ?, NULL, 1, ?, NULL)",
        [
            ("desktop", "Claude Desktop", ago(days=120)),
            ("cli", "MCP CLI", ago(days=120)),
        ],
    )

    # 1. Compte actif : jetons Garmin rafraîchis il y a deux heures.
    _principal(connection, "p-active", "alice@exemple.fr", ago(days=60), ago(days=2), linked=True)
    connection.execute(
        "INSERT INTO garmin_token_sets VALUES ('p-active', 1, 4, 1, X'00', ?)", (ago(hours=2),)
    )
    _consent(connection, "p-active", "desktop", ago(days=60))
    _privacy(connection, "p-active", "hash-v2", "2026-09-06", ago(days=60))
    _approval(connection, "p-active", "approved", ago(days=60), "admin", "collègue")
    _family(connection, "f-active", "p-active", "desktop", ago(days=60))
    _token(connection, "t-active-1", "f-active", "access", ago(days=30))
    _token(connection, "t-active-2", "f-active", "refresh", ago(hours=6))
    connection.execute(
        "INSERT INTO auth_codes VALUES ('c1', 'p-active', 'desktop', 'https://x/cb',"
        " 'garmin:read', 'aud', 'chal', ?, ?, ?)",
        (ago(days=60), ago(days=60), ago(days=60)),
    )

    # 2. Compte inactif : dernier jeton MCP il y a douze jours.
    _principal(connection, "p-idle", "bob@exemple.fr", ago(days=200), ago(days=200), linked=True)
    _consent(connection, "p-idle", "cli", ago(days=200))
    # Ce compte a vu deux versions successives de la notice.
    _approval(connection, "p-idle", "approved", ago(days=200), "migration", "")
    _privacy(connection, "p-idle", "hash-v1", "2026-01-01", ago(days=200))
    _privacy(connection, "p-idle", "hash-v2", "2026-09-06", ago(days=30))
    _family(connection, "f-idle", "p-idle", "cli", ago(days=200))
    _token(connection, "t-idle", "f-idle", "access", ago(days=12))

    # p-dormant reste en attente et p-new est bloqué : les trois états sont couverts.
    # 3. Compte dormant : plus rien depuis le consentement, il y a 300 jours.
    _principal(connection, "p-dormant", "carol@exemple.fr", ago(days=300), ago(days=300), True)
    _consent(connection, "p-dormant", "cli", ago(days=300))

    # 4. Compte jamais connecté : créé, jamais lié, aucun signal postérieur.
    _principal(connection, "p-new", "dan@exemple.fr", ago(days=1), ago(days=1), linked=False)
    _approval(connection, "p-new", "blocked", ago(hours=2), "admin", "inconnu au bataillon")

    connection.commit()
    connection.close()
    return path


def _principal(
    connection: sqlite3.Connection,
    principal_id: str,
    email: str,
    created: str,
    updated: str,
    linked: bool,
) -> None:
    connection.execute(
        "INSERT INTO principals (id, email_normalized, garmin_account_hash,"
        " garmin_identity_sealed, key_version, created_at, updated_at)"
        " VALUES (?, ?, ?, ?, 1, ?, ?)",
        (
            principal_id,
            email,
            f"hash-{principal_id}" if linked else None,
            b"\x00sealed" if linked else None,
            created,
            updated,
        ),
    )


def _approval(
    connection: sqlite3.Connection,
    principal_id: str,
    state: str,
    decided: str,
    decided_by: str,
    note: str,
) -> None:
    connection.execute(
        "INSERT INTO account_approvals VALUES (?, ?, ?, ?, ?)",
        (principal_id, state, decided, decided_by, note),
    )


def _privacy(
    connection: sqlite3.Connection,
    principal_id: str,
    notice_hash: str,
    version: str,
    accepted: str,
) -> None:
    connection.execute(
        "INSERT INTO privacy_notice_consents VALUES (?, ?, ?, ?)",
        (principal_id, notice_hash, version, accepted),
    )


def _consent(
    connection: sqlite3.Connection, principal_id: str, client_id: str, granted: str
) -> None:
    connection.execute(
        "INSERT INTO consents VALUES (?, ?, 'https://x/cb', '', 'garmin:read', ?, NULL)",
        (principal_id, client_id, granted),
    )


def _family(
    connection: sqlite3.Connection,
    family_id: str,
    principal_id: str,
    client_id: str,
    created: str,
) -> None:
    connection.execute(
        "INSERT INTO token_families VALUES (?, ?, ?, ?, NULL, NULL, '')",
        (family_id, principal_id, client_id, created),
    )


def _token(connection: sqlite3.Connection, token: str, family: str, kind: str, issued: str) -> None:
    connection.execute(
        "INSERT INTO mcp_tokens VALUES (?, ?, ?, 'garmin:read', 'aud', ?, ?, NULL, NULL, 0)",
        (token, family, kind, issued, issued),
    )
