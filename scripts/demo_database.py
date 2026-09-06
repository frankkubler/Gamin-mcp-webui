#!/usr/bin/env python3
"""Genere une base de demonstration au schema de garmin-mcp.

Sert a essayer l'interface sans toucher a une base de production :

    python scripts/demo_database.py /tmp/demo.db
    WEBUI_DATABASE_PATH=/tmp/demo.db WEBUI_PASSWORD=demo python -m app

Les donnees sont inventees. Aucune valeur chiffree n'est reelle : les colonnes
scellees recoivent des octets de remplissage, que l'interface ne lit pas.
"""

from __future__ import annotations

import random
import sqlite3
import sys
from datetime import UTC, datetime, timedelta
from pathlib import Path

SCHEMA = Path(__file__).resolve().parent.parent / "tests" / "schema.sql"

PRENOMS = [
    "alice",
    "bruno",
    "chloe",
    "damien",
    "elodie",
    "farid",
    "gaelle",
    "hugo",
    "ines",
    "julien",
    "karim",
    "lea",
]


def iso(moment: datetime) -> str:
    return moment.astimezone(UTC).strftime("%Y-%m-%dT%H:%M:%SZ")


def build(path: Path, seed: int = 7) -> None:
    if path.exists():
        path.unlink()
    rng = random.Random(seed)
    now = datetime.now(UTC)

    connection = sqlite3.connect(path)
    connection.executescript(SCHEMA.read_text(encoding="utf-8"))
    connection.execute(
        "INSERT INTO schema_migrations VALUES (1, 'initial', 'demo', ?)", (iso(now),)
    )
    connection.execute(
        "INSERT INTO schema_migrations VALUES (2, 'oauth_contract', 'demo', ?)", (iso(now),)
    )

    clients = [("desktop", "Claude Desktop"), ("cli", "MCP CLI"), ("mobile", "Application mobile")]
    for client_id, name in clients:
        connection.execute(
            "INSERT INTO oauth_clients (id, name, secret_hash, is_public, created_at, disabled_at)"
            " VALUES (?, ?, NULL, 1, ?, NULL)",
            (client_id, name, iso(now - timedelta(days=400))),
        )

    for index, prenom in enumerate(PRENOMS):
        principal_id = f"p-{index:03d}"
        created = now - timedelta(days=rng.randint(1, 400), hours=rng.randint(0, 23))
        linked = index % 5 != 0
        # Un cinquieme des comptes ne s'est jamais connecte apres la creation.
        last_activity = (
            None
            if index % 5 == 0
            else now - timedelta(hours=rng.choice([1, 5, 30, 100, 300, 900, 4000]))
        )
        if last_activity is not None and last_activity < created:
            last_activity = created + timedelta(hours=1)

        connection.execute(
            "INSERT INTO principals (id, email_normalized, garmin_account_hash,"
            " garmin_identity_sealed, key_version, created_at, updated_at) VALUES"
            " (?, ?, ?, ?, 1, ?, ?)",
            (
                principal_id,
                f"{prenom}@exemple.fr",
                f"hash-{principal_id}" if linked else None,
                b"\x00demo" if linked else None,
                iso(created),
                iso(last_activity or created),
            ),
        )

        if last_activity is None:
            continue

        # La notice de confidentialite : la plupart des comptes l'ont acceptee, et
        # un compte sur quatre est anterieur a sa mise en place, ce qui est l'etat
        # qu'un operateur doit pouvoir reperer dans l'interface.
        if index % 4 != 1:
            connection.execute(
                "INSERT INTO privacy_notice_consents VALUES (?, ?, ?, ?)",
                (
                    principal_id,
                    f"{index:064d}",
                    "2026-09-06-fr",
                    iso(created + timedelta(minutes=4)),
                ),
            )

        # La validation par l'operateur : la plupart des comptes sont valides, un
        # sur quatre attend encore, et un est bloque — les trois etats que
        # l'interface doit savoir montrer.
        if index % 4 != 1:
            connection.execute(
                "INSERT INTO account_approvals VALUES (?, ?, ?, 'admin', ?)",
                (
                    principal_id,
                    "blocked" if index % 7 == 3 else "approved",
                    iso(created + timedelta(minutes=6)),
                    "compte bloque" if index % 7 == 3 else "",
                ),
            )

        client_id = clients[index % len(clients)][0]
        connection.execute(
            "INSERT INTO consents VALUES"
            " (?, ?, 'https://exemple.fr/cb', '', 'garmin:read', ?, NULL)",
            (principal_id, client_id, iso(created + timedelta(minutes=5))),
        )
        if linked:
            connection.execute(
                "INSERT INTO garmin_token_sets VALUES (?, 1, ?, 1, X'00', ?)",
                (principal_id, rng.randint(1, 40), iso(last_activity)),
            )
        family_id = f"f-{index:03d}"
        connection.execute(
            "INSERT INTO token_families VALUES (?, ?, ?, ?, NULL, NULL, '')",
            (family_id, principal_id, client_id, iso(created + timedelta(minutes=5))),
        )
        for generation in range(rng.randint(1, 4)):
            issued = last_activity - timedelta(hours=generation * 6)
            connection.execute(
                "INSERT INTO mcp_tokens VALUES"
                " (?, ?, ?, 'garmin:read', 'aud', ?, ?, NULL, NULL, ?)",
                (
                    f"t-{index:03d}-{generation}",
                    family_id,
                    "access" if generation % 2 == 0 else "refresh",
                    iso(issued),
                    iso(issued + timedelta(hours=1)),
                    generation,
                ),
            )

    connection.commit()
    connection.close()
    print(f"Base de demonstration ecrite dans {path}")


if __name__ == "__main__":
    build(Path(sys.argv[1] if len(sys.argv) > 1 else "demo.db"))
