"""Configuration de l'interface web, lue dans l'environnement.

Toutes les variables sont prefixees par ``WEBUI_``. La seule valeur obligatoire
est le mot de passe d'acces : l'interface expose des adresses e-mail, elle ne
demarre donc pas sans authentification a moins d'un opt-in explicite.
"""

from __future__ import annotations

import os
from dataclasses import dataclass

DEFAULT_DATABASE_PATH = "/data/garmin.db"


class ConfigError(RuntimeError):
    """La configuration fournie ne permet pas de demarrer."""


def _flag(name: str, default: bool = False) -> bool:
    raw = os.environ.get(name)
    if raw is None or raw == "":
        return default
    return raw.strip().lower() in {"1", "true", "yes", "on", "oui"}


def _positive_int(name: str, default: int) -> int:
    raw = os.environ.get(name)
    if raw is None or raw == "":
        return default
    try:
        value = int(raw)
    except ValueError as exc:
        raise ConfigError(f"{name} doit être un entier, reçu {raw!r}") from exc
    if value <= 0:
        raise ConfigError(f"{name} doit être strictement positif, reçu {value}")
    return value


def _positive_float(name: str, default: float) -> float:
    raw = os.environ.get(name)
    if raw is None or raw == "":
        return default
    try:
        value = float(raw)
    except ValueError as exc:
        raise ConfigError(f"{name} doit être un nombre, reçu {raw!r}") from exc
    if value < 0:
        raise ConfigError(f"{name} ne peut pas être négatif, reçu {value}")
    return value


@dataclass(frozen=True)
class Settings:
    """Reglages effectifs du service."""

    database_path: str = DEFAULT_DATABASE_PATH
    username: str = "admin"
    password: str = ""
    api_token: str = ""
    allow_anonymous: bool = False
    mask_emails: bool = False
    snapshot_ttl_seconds: float = 5.0
    active_days: int = 7
    idle_days: int = 30
    title: str = "Comptes garmin-mcp"

    def __post_init__(self) -> None:
        if self.idle_days <= self.active_days:
            raise ConfigError(
                "WEBUI_IDLE_DAYS doit être supérieur à WEBUI_ACTIVE_DAYS "
                f"({self.idle_days} <= {self.active_days})"
            )
        if not self.allow_anonymous and not self.password and not self.api_token:
            raise ConfigError(
                "Aucun secret d'accès configuré. Définissez WEBUI_PASSWORD "
                "(ou WEBUI_API_TOKEN), ou acceptez explicitement un accès ouvert "
                "avec WEBUI_ALLOW_ANONYMOUS=1 — à ne faire que derrière un "
                "reverse proxy qui authentifie déjà."
            )


def load_settings(environ: dict[str, str] | None = None) -> Settings:
    """Construit les reglages a partir de l'environnement du processus."""

    if environ is not None:
        # Utilise par les tests : on isole la lecture sans toucher os.environ.
        previous = dict(os.environ)
        os.environ.clear()
        os.environ.update(environ)
        try:
            return load_settings()
        finally:
            os.environ.clear()
            os.environ.update(previous)

    return Settings(
        database_path=os.environ.get("WEBUI_DATABASE_PATH", DEFAULT_DATABASE_PATH),
        username=os.environ.get("WEBUI_USERNAME", "admin"),
        password=os.environ.get("WEBUI_PASSWORD", ""),
        api_token=os.environ.get("WEBUI_API_TOKEN", ""),
        allow_anonymous=_flag("WEBUI_ALLOW_ANONYMOUS"),
        mask_emails=_flag("WEBUI_MASK_EMAILS"),
        snapshot_ttl_seconds=_positive_float("WEBUI_SNAPSHOT_TTL_SECONDS", 5.0),
        active_days=_positive_int("WEBUI_ACTIVE_DAYS", 7),
        idle_days=_positive_int("WEBUI_IDLE_DAYS", 30),
        title=os.environ.get("WEBUI_TITLE", "Comptes garmin-mcp"),
    )
