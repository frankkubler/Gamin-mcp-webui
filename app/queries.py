"""Lecture des comptes garmin-mcp et derivation de la « derniere connexion ».

Le schema de garmin-mcp ne stocke pas de colonne « derniere connexion » : il
stocke des faits horodates. Ce module lit ces faits et en derive la date la plus
recente, en gardant la trace du signal gagnant pour que l'interface puisse dire
d'ou vient la valeur affichee.

Signaux utilises, du plus revelateur d'un usage reel au plus faible :

===========================  ===============================================
Signal                       Origine
===========================  ===============================================
garmin_tokens_refreshed_at   garmin_token_sets.updated_at — les jetons Garmin
                             sont reecrits a chaque rafraichissement, donc a
                             chaque usage effectif de l'API Garmin.
mcp_token_issued_at          max(mcp_tokens.issued_at) sur les familles du
                             compte — emission d'un jeton d'acces ou d'un
                             refresh par le client MCP.
authorization_code_at        max(auth_codes.created_at) — passage complet par
                             la page de login dans le navigateur.
consent_granted_at           max(consents.granted_at) — consentement accorde
                             a un client MCP.
audit_event_at               max(audit_events.occurred_at) — table d'audit,
                             vide tant que le serveur n'y ecrit pas.
principal_updated_at         principals.updated_at — repli : liaison Garmin
                             creee ou modifiee.
===========================  ===============================================

Attention a la retention : la tache de nettoyage de garmin-mcp supprime les
jetons et les codes expires. Un compte inactif depuis longtemps peut donc voir
sa « derniere connexion » retomber sur un signal plus ancien mais persistant
(consentement, jetons Garmin). Les consentements, eux, ne sont jamais supprimes.

Le consentement a la notice de confidentialite est lu dans la table
``privacy_notice_consents``, ajoutee par la migration 0003 du serveur : une ligne par
compte et par texte accepte, avec l'empreinte de ce texte, son libelle de version et
l'instant de l'acceptation. Un compte sans ligne n'a jamais accepte de notice, ce qui
est l'etat normal d'un compte anterieur a sa mise en place.

Aucune colonne sensible ne sort d'ici : ni empreinte (``*_hash``), ni enveloppe
chiffree (``*_sealed``). Seul l'e-mail, que garmin-mcp stocke en clair comme
identifiant de connexion, est expose — et masquable via WEBUI_MASK_EMAILS.
"""

from __future__ import annotations

import sqlite3
from datetime import UTC, datetime, timedelta

# Ordre de preference quand plusieurs signaux portent la meme date : le premier
# de cette liste est retenu comme source affichee.
SIGNAL_FIELDS: tuple[str, ...] = (
    "garmin_tokens_refreshed_at",
    "mcp_token_issued_at",
    "authorization_code_at",
    "consent_granted_at",
    "audit_event_at",
    "principal_updated_at",
)

SIGNAL_LABELS: dict[str, str] = {
    "garmin_tokens_refreshed_at": "Rafraîchissement des jetons Garmin",
    "mcp_token_issued_at": "Émission d'un jeton MCP",
    "authorization_code_at": "Autorisation dans le navigateur",
    "consent_granted_at": "Consentement accordé",
    "audit_event_at": "Événement d'audit",
    "principal_updated_at": "Mise à jour du compte",
}

_ACCOUNTS_SQL = """
SELECT
    p.id                                        AS id,
    p.email_normalized                          AS email,
    CASE WHEN p.garmin_account_hash IS NOT NULL
         THEN 1 ELSE 0 END                      AS garmin_linked,
    p.created_at                                AS created_at,
    p.updated_at                                AS principal_updated_at,
    (SELECT g.updated_at
       FROM garmin_token_sets g
      WHERE g.principal_id = p.id)              AS garmin_tokens_refreshed_at,
    (SELECT MAX(t.issued_at)
       FROM mcp_tokens t
       JOIN token_families f ON f.id = t.family_id
      WHERE f.principal_id = p.id)              AS mcp_token_issued_at,
    (SELECT MAX(a.created_at)
       FROM auth_codes a
      WHERE a.principal_id = p.id)              AS authorization_code_at,
    (SELECT MAX(c.granted_at)
       FROM consents c
      WHERE c.principal_id = p.id)              AS consent_granted_at,
    (SELECT MAX(e.occurred_at)
       FROM audit_events e
      WHERE e.principal_id = p.id)              AS audit_event_at,
    (SELECT COUNT(*)
       FROM token_families f
      WHERE f.principal_id = p.id)              AS token_families_total,
    (SELECT COUNT(*)
       FROM token_families f
      WHERE f.principal_id = p.id
        AND f.revoked_at IS NULL)               AS token_families_active,
    (SELECT COUNT(DISTINCT c.client_id)
       FROM consents c
      WHERE c.principal_id = p.id
        AND c.revoked_at IS NULL)               AS clients_authorized,
    (SELECT MAX(n.accepted_at)
       FROM privacy_notice_consents n
      WHERE n.principal_id = p.id)              AS privacy_accepted_at,
    (SELECT n.notice_version
       FROM privacy_notice_consents n
      WHERE n.principal_id = p.id
      ORDER BY n.accepted_at DESC, n.notice_hash
      LIMIT 1)                                  AS privacy_notice_version,
    (SELECT COUNT(*)
       FROM privacy_notice_consents n
      WHERE n.principal_id = p.id)              AS privacy_acceptances
FROM principals p
ORDER BY p.created_at DESC, p.id
"""

_CONSENTS_SQL = """
SELECT
    c.client_id     AS client_id,
    o.name          AS client_name,
    c.scopes        AS scopes,
    c.redirect_uri  AS redirect_uri,
    c.resource      AS resource,
    c.granted_at    AS granted_at,
    c.revoked_at    AS revoked_at
FROM consents c
LEFT JOIN oauth_clients o ON o.id = c.client_id
WHERE c.principal_id = ?
ORDER BY c.granted_at DESC
"""

_FAMILIES_SQL = """
SELECT
    f.id                AS id,
    f.client_id         AS client_id,
    o.name              AS client_name,
    f.resource          AS resource,
    f.created_at        AS created_at,
    f.revoked_at        AS revoked_at,
    f.revocation_reason AS revocation_reason,
    (SELECT COUNT(*) FROM mcp_tokens t WHERE t.family_id = f.id)        AS tokens,
    (SELECT MAX(t.issued_at) FROM mcp_tokens t WHERE t.family_id = f.id) AS last_token_issued_at,
    (SELECT MAX(t.expires_at)
       FROM mcp_tokens t
      WHERE t.family_id = f.id
        AND t.kind = 'access'
        AND t.revoked_at IS NULL
        AND t.consumed_at IS NULL)                                       AS access_expires_at
FROM token_families f
LEFT JOIN oauth_clients o ON o.id = f.client_id
WHERE f.principal_id = ?
ORDER BY f.created_at DESC
"""

_PRIVACY_SQL = """
SELECT
    n.notice_version AS notice_version,
    n.notice_hash    AS notice_hash,
    n.accepted_at    AS accepted_at
FROM privacy_notice_consents n
WHERE n.principal_id = ?
ORDER BY n.accepted_at DESC, n.notice_hash
"""

_AUDIT_SQL = """
SELECT
    e.occurred_at AS occurred_at,
    e.kind        AS kind,
    e.outcome     AS outcome,
    e.client_id   AS client_id,
    e.detail      AS detail
FROM audit_events e
WHERE e.principal_id = ?
ORDER BY e.occurred_at DESC
LIMIT ?
"""


def parse_timestamp(value: str | None) -> datetime | None:
    """Lit un horodatage RFC 3339 UTC tel que garmin-mcp les ecrit."""

    if not value:
        return None
    text = value.strip()
    if text.endswith(("Z", "z")):
        text = text[:-1] + "+00:00"
    try:
        parsed = datetime.fromisoformat(text)
    except ValueError:
        return None
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=UTC)
    return parsed.astimezone(UTC)


def _iso(moment: datetime | None) -> str | None:
    if moment is None:
        return None
    return moment.astimezone(UTC).isoformat().replace("+00:00", "Z")


def mask_email(email: str | None) -> str | None:
    """Masque un e-mail pour l'affichage : ``jean.dupont@exemple.fr`` -> ``j***@e***.fr``."""

    if not email:
        return email
    local, separator, domain = email.partition("@")
    masked_local = (local[0] + "***") if local else "***"
    if not separator:
        return masked_local
    name, dot, tld = domain.rpartition(".")
    if not dot:
        return f"{masked_local}@{(domain[0] + '***') if domain else '***'}"
    masked_domain = (name[0] + "***") if name else "***"
    return f"{masked_local}@{masked_domain}.{tld}"


def derive_last_seen(row: dict[str, object]) -> tuple[datetime | None, str | None]:
    """Retourne la date de derniere activite et le signal qui la porte.

    ``principals.updated_at`` vaut ``created_at`` tant que rien n'a bouge sur le
    compte : le compter comme une connexion ferait passer pour actif un compte
    tout juste cree qui ne s'est jamais connecte. Il n'est donc retenu que s'il
    est strictement posterieur a la creation. Un compte sans aucun autre signal
    ressort avec ``None``, ce qui est l'etat « jamais connecte ».
    """

    created_at = parse_timestamp(row.get("created_at"))  # type: ignore[arg-type]
    best: datetime | None = None
    source: str | None = None

    for field in SIGNAL_FIELDS:
        moment = parse_timestamp(row.get(field))  # type: ignore[arg-type]
        if moment is None:
            continue
        if field == "principal_updated_at" and created_at is not None and moment <= created_at:
            continue
        if best is None or moment > best:
            best = moment
            source = field

    return best, source


def classify(
    last_seen: datetime | None,
    now: datetime,
    active_days: int,
    idle_days: int,
) -> str:
    """Classe un compte : ``actif``, ``inactif``, ``dormant`` ou ``jamais_connecte``."""

    if last_seen is None:
        return "jamais_connecte"
    age = now - last_seen
    if age <= timedelta(days=active_days):
        return "actif"
    if age <= timedelta(days=idle_days):
        return "inactif"
    return "dormant"


def list_accounts(
    connection: sqlite3.Connection,
    *,
    now: datetime | None = None,
    active_days: int = 7,
    idle_days: int = 30,
    mask_emails: bool = False,
) -> list[dict[str, object]]:
    """Lit tous les comptes et calcule leur derniere connexion."""

    moment = now or datetime.now(UTC)
    accounts: list[dict[str, object]] = []

    for raw in connection.execute(_ACCOUNTS_SQL):
        row = dict(raw)
        last_seen, source = derive_last_seen(row)
        created_at = parse_timestamp(row.get("created_at"))  # type: ignore[arg-type]
        email = row.get("email")
        account: dict[str, object] = {
            "id": row["id"],
            "email": mask_email(email) if mask_emails else email,  # type: ignore[arg-type]
            "garmin_linked": bool(row["garmin_linked"]),
            "created_at": _iso(created_at),
            "created_days_ago": _days_between(created_at, moment),
            "last_seen_at": _iso(last_seen),
            "last_seen_source": source,
            "last_seen_label": SIGNAL_LABELS.get(source or ""),
            "last_seen_days_ago": _days_between(last_seen, moment),
            "status": classify(last_seen, moment, active_days, idle_days),
            "clients_authorized": int(row["clients_authorized"]),
            "privacy_accepted_at": _iso(
                parse_timestamp(row.get("privacy_accepted_at"))  # type: ignore[arg-type]
            ),
            "privacy_notice_version": row.get("privacy_notice_version"),
            "privacy_acceptances": int(row["privacy_acceptances"]),
            "privacy_consent": bool(row["privacy_acceptances"]),
            "token_families_active": int(row["token_families_active"]),
            "token_families_total": int(row["token_families_total"]),
            "signals": {
                field: _iso(parse_timestamp(row.get(field)))  # type: ignore[arg-type]
                for field in SIGNAL_FIELDS
            },
        }
        accounts.append(account)

    accounts.sort(key=_sort_key_last_seen, reverse=True)
    return accounts


def _sort_key_last_seen(account: dict[str, object]) -> tuple[int, str]:
    value = account.get("last_seen_at")
    if isinstance(value, str):
        return (1, value)
    return (0, "")


def _days_between(moment: datetime | None, now: datetime) -> float | None:
    if moment is None:
        return None
    return round((now - moment).total_seconds() / 86400.0, 2)


def get_account(
    connection: sqlite3.Connection,
    principal_id: str,
    *,
    now: datetime | None = None,
    active_days: int = 7,
    idle_days: int = 30,
    mask_emails: bool = False,
    audit_limit: int = 50,
) -> dict[str, object] | None:
    """Detail d'un compte : signaux, clients autorises, familles de jetons, audit."""

    accounts = list_accounts(
        connection,
        now=now,
        active_days=active_days,
        idle_days=idle_days,
        mask_emails=mask_emails,
    )
    account = next((item for item in accounts if item["id"] == principal_id), None)
    if account is None:
        return None

    account = dict(account)
    account["consents"] = [dict(row) for row in connection.execute(_CONSENTS_SQL, (principal_id,))]
    account["privacy_notice_consents"] = [
        dict(row) for row in connection.execute(_PRIVACY_SQL, (principal_id,))
    ]
    account["token_families"] = [
        dict(row) for row in connection.execute(_FAMILIES_SQL, (principal_id,))
    ]
    account["audit_events"] = [
        dict(row) for row in connection.execute(_AUDIT_SQL, (principal_id, audit_limit))
    ]
    return account


def summarize(
    accounts: list[dict[str, object]],
    *,
    now: datetime | None = None,
    active_days: int = 7,
    idle_days: int = 30,
) -> dict[str, object]:
    """Agrege les compteurs affiches en tete de l'interface."""

    moment = now or datetime.now(UTC)
    counters = {"actif": 0, "inactif": 0, "dormant": 0, "jamais_connecte": 0}
    linked = 0
    consented = 0
    created_7d = 0
    created_30d = 0
    seen_24h = 0
    seen_7d = 0
    latest: str | None = None

    for account in accounts:
        counters[str(account["status"])] += 1
        if account["garmin_linked"]:
            linked += 1
        if account.get("privacy_consent"):
            consented += 1

        created_age = account.get("created_days_ago")
        if isinstance(created_age, (int, float)):
            if created_age <= 7:
                created_7d += 1
            if created_age <= 30:
                created_30d += 1

        seen_age = account.get("last_seen_days_ago")
        if isinstance(seen_age, (int, float)):
            if seen_age <= 1:
                seen_24h += 1
            if seen_age <= 7:
                seen_7d += 1

        last_seen = account.get("last_seen_at")
        if isinstance(last_seen, str) and (latest is None or last_seen > latest):
            latest = last_seen

    return {
        "generated_at": _iso(moment),
        "accounts_total": len(accounts),
        "garmin_linked": linked,
        "privacy_consent": consented,
        "privacy_consent_missing": len(accounts) - consented,
        "created_last_7_days": created_7d,
        "created_last_30_days": created_30d,
        "seen_last_24_hours": seen_24h,
        "seen_last_7_days": seen_7d,
        "last_activity_at": latest,
        "by_status": counters,
        "thresholds": {"active_days": active_days, "idle_days": idle_days},
    }
