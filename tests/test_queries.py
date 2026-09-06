"""La dérivation « dernière connexion » et les agrégats."""

from __future__ import annotations

import sqlite3
from datetime import UTC, datetime
from pathlib import Path

import pytest

from app import queries
from app.db import Database, DatabaseUnavailable


@pytest.fixture
def accounts(database_path: Path, now: datetime) -> list[dict[str, object]]:
    with Database(database_path).connect() as connection:
        return queries.list_accounts(connection, now=now)


def by_id(accounts: list[dict[str, object]], principal_id: str) -> dict[str, object]:
    return next(account for account in accounts if account["id"] == principal_id)


def test_liste_tous_les_comptes(accounts: list[dict[str, object]]) -> None:
    assert {account["id"] for account in accounts} == {
        "p-active",
        "p-idle",
        "p-dormant",
        "p-new",
    }


def test_derniere_connexion_prend_le_signal_le_plus_recent(
    accounts: list[dict[str, object]],
) -> None:
    active = by_id(accounts, "p-active")
    # Le rafraîchissement Garmin (2 h) est plus récent que le jeton MCP (6 h).
    assert active["last_seen_at"] == "2026-09-06T10:00:00Z"
    assert active["last_seen_source"] == "garmin_tokens_refreshed_at"
    assert active["status"] == "actif"


def test_repli_sur_le_jeton_mcp_quand_garmin_est_muet(
    accounts: list[dict[str, object]],
) -> None:
    idle = by_id(accounts, "p-idle")
    assert idle["last_seen_source"] == "mcp_token_issued_at"
    assert idle["status"] == "inactif"


def test_repli_sur_le_consentement_quand_les_jetons_sont_purges(
    accounts: list[dict[str, object]],
) -> None:
    dormant = by_id(accounts, "p-dormant")
    assert dormant["last_seen_source"] == "consent_granted_at"
    assert dormant["status"] == "dormant"


def test_compte_sans_signal_est_jamais_connecte(accounts: list[dict[str, object]]) -> None:
    fresh = by_id(accounts, "p-new")
    # updated_at == created_at : rien ne s'est passé depuis la création.
    assert fresh["status"] == "jamais_connecte"
    assert fresh["garmin_linked"] is False


def test_tri_par_defaut_du_plus_recent_au_plus_ancien(
    accounts: list[dict[str, object]],
) -> None:
    identifiers = [account["id"] for account in accounts]
    assert identifiers[:3] == ["p-active", "p-idle", "p-dormant"]
    assert identifiers[-1] == "p-new"


def test_les_secrets_ne_sortent_jamais(accounts: list[dict[str, object]]) -> None:
    serialized = repr(accounts)
    for forbidden in ("garmin_account_hash", "sealed", "hash-p-active", "token_hash"):
        assert forbidden not in serialized


def test_masquage_des_emails(database_path: Path, now: datetime) -> None:
    with Database(database_path).connect() as connection:
        masked = queries.list_accounts(connection, now=now, mask_emails=True)
    assert by_id(masked, "p-active")["email"] == "a***@e***.fr"


def test_detail_du_compte(database_path: Path, now: datetime) -> None:
    with Database(database_path).connect() as connection:
        detail = queries.get_account(connection, "p-active", now=now)
    assert detail is not None
    assert detail["consents"][0]["client_name"] == "Claude Desktop"
    assert detail["token_families"][0]["tokens"] == 2
    assert detail["token_families"][0]["last_token_issued_at"] == "2026-09-06T06:00:00Z"


def test_detail_inconnu(database_path: Path, now: datetime) -> None:
    with Database(database_path).connect() as connection:
        assert queries.get_account(connection, "absent", now=now) is None


def test_agregats(accounts: list[dict[str, object]], now: datetime) -> None:
    stats = queries.summarize(accounts, now=now)
    assert stats["accounts_total"] == 4
    assert stats["garmin_linked"] == 3
    assert stats["by_status"] == {
        "actif": 1,
        "inactif": 1,
        "dormant": 1,
        "jamais_connecte": 1,
    }
    assert stats["seen_last_24_hours"] == 1
    assert stats["created_last_7_days"] == 1


def test_seuils_configurables(database_path: Path, now: datetime) -> None:
    with Database(database_path).connect() as connection:
        accounts = queries.list_accounts(connection, now=now, active_days=1, idle_days=15)
    assert by_id(accounts, "p-active")["status"] == "actif"
    assert by_id(accounts, "p-idle")["status"] == "inactif"


@pytest.mark.parametrize(
    ("value", "expected"),
    [
        ("2026-09-06T10:00:00Z", datetime(2026, 9, 6, 10, tzinfo=UTC)),
        ("2026-09-06T10:00:00+02:00", datetime(2026, 9, 6, 8, tzinfo=UTC)),
        ("", None),
        (None, None),
        ("pas une date", None),
    ],
)
def test_lecture_des_horodatages(value: str | None, expected: datetime | None) -> None:
    assert queries.parse_timestamp(value) == expected


@pytest.mark.parametrize(
    ("email", "expected"),
    [
        ("jean.dupont@exemple.fr", "j***@e***.fr"),
        ("a@b.co", "a***@b***.co"),
        ("sansarobase", "s***"),
        (None, None),
    ],
)
def test_masque(email: str | None, expected: str | None) -> None:
    assert queries.mask_email(email) == expected


def test_base_absente(tmp_path: Path) -> None:
    with pytest.raises(DatabaseUnavailable), Database(tmp_path / "rien.db").connect():
        pass


def test_lecture_seule(database_path: Path) -> None:
    with (
        Database(database_path).connect() as connection,
        pytest.raises(sqlite3.OperationalError),
    ):
        connection.execute("DELETE FROM principals")


def test_repli_sur_instantane_quand_la_lecture_directe_echoue(
    database_path: Path, monkeypatch: pytest.MonkeyPatch, now: datetime
) -> None:
    """Une base WAL sur un montage en lecture seule passe par la copie."""

    database = Database(database_path, snapshot_ttl_seconds=0.0)
    real_open = database._open
    calls: list[Path] = []

    def fake_open(path: Path):  # type: ignore[no-untyped-def]
        calls.append(path)
        if path == database_path:
            raise sqlite3.OperationalError("attempt to write a readonly database")
        return real_open(path)

    monkeypatch.setattr(database, "_open", fake_open)
    with database.connect() as connection:
        accounts = queries.list_accounts(connection, now=now)

    assert len(accounts) == 4
    assert calls[0] == database_path and calls[1] != database_path
    assert database.info().access_mode == "snapshot"
    database.close()
