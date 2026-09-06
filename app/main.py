"""Application FastAPI : API JSON + page unique de consultation.

L'interface est strictement en lecture. Aucune route n'ecrit dans la base de
garmin-mcp, et aucune ne renvoie une empreinte (``*_hash``) ou une enveloppe
chiffree (``*_sealed``).
"""

from __future__ import annotations

import csv
import io
from collections.abc import AsyncIterator
from contextlib import asynccontextmanager
from datetime import UTC, datetime
from pathlib import Path
from typing import Any, Literal

from fastapi import Depends, FastAPI, HTTPException, Query, Request, status
from fastapi.responses import FileResponse, JSONResponse, StreamingResponse
from fastapi.staticfiles import StaticFiles

from . import queries
from .auth import authorize
from .config import Settings, load_settings
from .db import Database, DatabaseReadOnly, DatabaseUnavailable

STATIC_DIR = Path(__file__).parent / "static"

SORTABLE_FIELDS = {
    "last_seen_at": "last_seen_at",
    "created_at": "created_at",
    "email": "email",
    "status": "status",
    "clients_authorized": "clients_authorized",
    "privacy_accepted_at": "privacy_accepted_at",
}


def create_app(settings: Settings | None = None) -> FastAPI:
    """Construit l'application. Les tests injectent leurs propres reglages."""

    resolved = settings or load_settings()

    @asynccontextmanager
    async def lifespan(instance: FastAPI) -> AsyncIterator[None]:
        yield
        instance.state.database.close()

    app = FastAPI(
        lifespan=lifespan,
        title=resolved.title,
        description=(
            "Consultation en lecture seule des comptes d'un déploiement "
            "garmin-mcp distant : création de compte et dernière connexion."
        ),
        docs_url="/api/docs",
        openapi_url="/api/openapi.json",
    )
    app.state.settings = resolved
    app.state.database = Database(
        resolved.database_path, snapshot_ttl_seconds=resolved.snapshot_ttl_seconds
    )

    @app.exception_handler(DatabaseReadOnly)
    def _read_only(_: Request, exc: DatabaseReadOnly) -> JSONResponse:
        return JSONResponse(status_code=status.HTTP_409_CONFLICT, content={"detail": str(exc)})

    @app.exception_handler(queries.ApprovalRefused)
    def _refused(_: Request, exc: queries.ApprovalRefused) -> JSONResponse:
        return JSONResponse(
            status_code=status.HTTP_422_UNPROCESSABLE_ENTITY, content={"detail": str(exc)}
        )

    @app.exception_handler(DatabaseUnavailable)
    def _unavailable(_: Request, exc: DatabaseUnavailable) -> JSONResponse:
        return JSONResponse(
            status_code=status.HTTP_503_SERVICE_UNAVAILABLE,
            content={"detail": str(exc)},
        )

    _register_routes(app)

    if STATIC_DIR.is_dir():
        app.mount("/static", StaticFiles(directory=STATIC_DIR), name="static")

    return app


def _accounts(request: Request) -> list[dict[str, Any]]:
    settings: Settings = request.app.state.settings
    database: Database = request.app.state.database
    with database.connect() as connection:
        return queries.list_accounts(
            connection,
            active_days=settings.active_days,
            idle_days=settings.idle_days,
            mask_emails=settings.mask_emails,
        )


def _register_routes(app: FastAPI) -> None:
    protected = [Depends(authorize)]

    @app.get("/", include_in_schema=False)
    def index() -> FileResponse:
        page = STATIC_DIR / "index.html"
        if not page.is_file():
            raise HTTPException(status_code=404, detail="Page absente.")
        return FileResponse(page)

    @app.get("/api/health", tags=["service"])
    def health(request: Request) -> dict[str, Any]:
        """Sonde publique : ne révèle que l'état de lisibilité de la base."""

        info = request.app.state.database.info()
        return {"status": "ok" if info.readable else "degraded", "database_readable": info.readable}

    @app.get("/api/status", tags=["service"], dependencies=protected)
    def service_status(request: Request) -> dict[str, Any]:
        """État détaillé : chemin, taille, mode d'accès, version de schéma."""

        settings: Settings = request.app.state.settings
        info = request.app.state.database.info()
        modified = (
            datetime.fromtimestamp(info.modified_at, tz=UTC).isoformat().replace("+00:00", "Z")
            if info.modified_at
            else None
        )
        return {
            "database": {
                "path": info.path,
                "exists": info.exists,
                "readable": info.readable,
                "size_bytes": info.size_bytes,
                "modified_at": modified,
                "access_mode": info.access_mode,
                "schema_version": info.schema_version,
            },
            "settings": {
                "active_days": settings.active_days,
                "idle_days": settings.idle_days,
                "mask_emails": settings.mask_emails,
                "anonymous_access": settings.allow_anonymous,
                "title": settings.title,
            },
            "signals": queries.SIGNAL_LABELS,
        }

    @app.get("/api/stats", tags=["comptes"], dependencies=protected)
    def stats(request: Request) -> dict[str, Any]:
        """Compteurs agrégés sur l'ensemble des comptes."""

        settings: Settings = request.app.state.settings
        return queries.summarize(
            _accounts(request),
            active_days=settings.active_days,
            idle_days=settings.idle_days,
        )

    @app.get("/api/accounts", tags=["comptes"], dependencies=protected)
    def accounts(
        request: Request,
        search: str = Query("", description="Filtre sur l'e-mail ou l'identifiant."),
        status_filter: Literal["", "actif", "inactif", "dormant", "jamais_connecte"] = Query(
            "", alias="status", description="Filtre sur l'état dérivé."
        ),
        linked: Literal["", "oui", "non"] = Query("", description="Compte lié à Garmin."),
        consent: Literal["", "oui", "non"] = Query(
            "", description="Notice de confidentialité acceptée."
        ),
        approval: Literal["", "pending", "approved", "blocked"] = Query(
            "", description="État de validation du compte."
        ),
        sort: str = Query("last_seen_at", description="Champ de tri."),
        order: Literal["asc", "desc"] = Query("desc"),
        limit: int = Query(100, ge=1, le=1000),
        offset: int = Query(0, ge=0),
    ) -> dict[str, Any]:
        """Liste paginée des comptes, avec leur dernière connexion dérivée."""

        if sort not in SORTABLE_FIELDS:
            raise HTTPException(
                status_code=422,
                detail=f"Tri inconnu : {sort}. Valeurs acceptées : {sorted(SORTABLE_FIELDS)}.",
            )

        items = _filter(_accounts(request), search, status_filter, linked, consent, approval)
        items = _sort(items, sort, order)
        return {
            "total": len(items),
            "limit": limit,
            "offset": offset,
            "items": items[offset : offset + limit],
        }

    @app.get("/api/accounts.csv", tags=["comptes"], dependencies=protected)
    def accounts_csv(
        request: Request,
        search: str = Query(""),
        status_filter: Literal["", "actif", "inactif", "dormant", "jamais_connecte"] = Query(
            "", alias="status"
        ),
        linked: Literal["", "oui", "non"] = Query(""),
        consent: Literal["", "oui", "non"] = Query(""),
        approval: Literal["", "pending", "approved", "blocked"] = Query(""),
    ) -> StreamingResponse:
        """Export CSV de la même liste, pour un rapport ou un tableur."""

        items = _filter(_accounts(request), search, status_filter, linked, consent, approval)
        columns = [
            "id",
            "email",
            "garmin_linked",
            "created_at",
            "last_seen_at",
            "last_seen_source",
            "status",
            "clients_authorized",
            "token_families_active",
            "privacy_consent",
            "privacy_accepted_at",
            "privacy_notice_version",
            "approval_state",
            "approval_decided_at",
            "approval_decided_by",
        ]
        buffer = io.StringIO()
        writer = csv.DictWriter(buffer, fieldnames=columns, extrasaction="ignore")
        writer.writeheader()
        for item in _sort(items, "last_seen_at", "desc"):
            writer.writerow({column: _csv_value(item.get(column)) for column in columns})
        buffer.seek(0)
        stamp = datetime.now(UTC).strftime("%Y%m%d-%H%M")
        return StreamingResponse(
            iter([buffer.getvalue()]),
            media_type="text/csv; charset=utf-8",
            headers={
                "Content-Disposition": f'attachment; filename="garmin-mcp-comptes-{stamp}.csv"'
            },
        )

    @app.post(
        "/api/accounts/{principal_id}/approval",
        tags=["comptes"],
        # Le contrôle d'origine est une dépendance, donc il s'exécute avant que le
        # corps ne soit validé : une écriture venue d'ailleurs est refusée pour ce
        # qu'elle est, et non par accident parce que son corps n'était pas du JSON.
        dependencies=[*protected, Depends(_refuse_cross_site)],
        status_code=status.HTTP_200_OK,
    )
    def decide(
        request: Request,
        principal_id: str,
        decision: dict[str, Any],
    ) -> dict[str, Any]:
        """Valide, bloque, ou remet en attente un compte.

        Corps attendu : ``{"state": "approved" | "blocked" | "pending", "note": "…"}``.
        ``pending`` retire la décision, ce qui remet le compte en attente.

        C'est la seule écriture de toute l'interface, et elle ne touche qu'à la table
        des validations : la connexion installe un autorisateur SQLite qui refuse
        toute autre écriture avant même de l'exécuter.
        """

        settings: Settings = request.app.state.settings
        database: Database = request.app.state.database

        state = str(decision.get("state", "")).strip()
        note = str(decision.get("note", "") or "")

        with database.connect_write() as connection:
            try:
                recorded = queries.set_approval(
                    connection,
                    principal_id,
                    state,
                    decided_by=settings.username,
                    note=note,
                )
            except LookupError as absent:
                raise HTTPException(status_code=404, detail="Compte inconnu.") from absent
        return recorded

    @app.get("/api/accounts/{principal_id}", tags=["comptes"], dependencies=protected)
    def account(request: Request, principal_id: str) -> dict[str, Any]:
        """Détail d'un compte : signaux, clients autorisés, familles de jetons."""

        settings: Settings = request.app.state.settings
        database: Database = request.app.state.database
        with database.connect() as connection:
            detail = queries.get_account(
                connection,
                principal_id,
                active_days=settings.active_days,
                idle_days=settings.idle_days,
                mask_emails=settings.mask_emails,
            )
        if detail is None:
            raise HTTPException(status_code=404, detail="Compte inconnu.")
        return detail


def _refuse_cross_site(request: Request) -> None:
    """Refuse une écriture venue d'un autre site.

    L'interface s'authentifie en HTTP Basic, que le navigateur rejoue tout seul :
    sans ce contrôle, une page tierce pourrait faire valider un compte à l'insu de
    l'opérateur. Deux barrières, toutes deux nécessaires côté serveur :

    * l'en-tête ``Origin``, quand il est présent, doit désigner cet hôte ;
    * le corps doit être du JSON, ce qu'un formulaire HTML ne peut pas envoyer
      sans passer par un contrôle préalable CORS que rien n'autorise ici.
    """

    content_type = request.headers.get("content-type", "").split(";")[0].strip().lower()
    if content_type != "application/json":
        raise HTTPException(
            status_code=status.HTTP_415_UNSUPPORTED_MEDIA_TYPE,
            detail="Cette route n'accepte que du JSON (Content-Type: application/json).",
        )

    origin = request.headers.get("origin")
    if origin:
        host = request.headers.get("host", "")
        attendu = {f"http://{host}", f"https://{host}"}
        if origin not in attendu:
            raise HTTPException(
                status_code=status.HTTP_403_FORBIDDEN,
                detail="Origine refusée : cette écriture ne peut venir que de l'interface.",
            )


def _csv_value(value: Any) -> str:
    """Rend une valeur lisible dans un tableur : booléens en oui/non, vides en blanc."""

    if value is None:
        return ""
    if isinstance(value, bool):
        return "oui" if value else "non"
    return str(value)


def _filter(
    accounts: list[dict[str, Any]],
    search: str,
    status_filter: str,
    linked: str,
    consent: str = "",
    approval: str = "",
) -> list[dict[str, Any]]:
    needle = search.strip().lower()
    result = accounts
    if needle:
        result = [
            account
            for account in result
            if needle in str(account.get("email") or "").lower()
            or needle in str(account.get("id") or "").lower()
        ]
    if status_filter:
        result = [account for account in result if account["status"] == status_filter]
    if linked:
        wanted = linked == "oui"
        result = [account for account in result if bool(account["garmin_linked"]) is wanted]
    if consent:
        accepted = consent == "oui"
        result = [account for account in result if bool(account["privacy_consent"]) is accepted]
    if approval:
        result = [account for account in result if account["approval_state"] == approval]
    return result


def _sort(accounts: list[dict[str, Any]], sort: str, order: str) -> list[dict[str, Any]]:
    """Trie sur un champ, en gardant les valeurs absentes en fin de liste.

    Un compte sans derniere connexion n'a pas de place naturelle dans un ordre
    chronologique : le mettre en tete d'un tri croissant ferait passer pour le
    plus ancien un compte qui n'a simplement aucune donnee. Les valeurs nulles
    sont donc extraites du tri et rajoutees a la fin, dans les deux sens.
    """

    field = SORTABLE_FIELDS[sort]
    present = [account for account in accounts if account.get(field) is not None]
    missing = [account for account in accounts if account.get(field) is None]

    def key(account: dict[str, Any]) -> Any:
        value = account[field]
        if isinstance(value, str):
            return value.lower()
        if isinstance(value, bool):
            return int(value)
        return value

    present.sort(key=key, reverse=order == "desc")
    return present + missing
