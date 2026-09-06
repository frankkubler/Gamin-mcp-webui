"""Controle d'acces de l'interface.

Deux voies, volontairement simples parce que l'interface est un outil
d'exploitation : l'authentification HTTP Basic pour le navigateur, un jeton
porteur pour les appels programmatiques. Les comparaisons passent par
``secrets.compare_digest`` afin de ne pas fuir la longueur commune d'un prefixe.
"""

from __future__ import annotations

import base64
import binascii
import secrets

from fastapi import HTTPException, Request, status

from .config import Settings

_UNAUTHORIZED_HEADERS = {"WWW-Authenticate": 'Basic realm="garmin-mcp web UI", charset="UTF-8"'}


def _equal(left: str, right: str) -> bool:
    return secrets.compare_digest(left.encode("utf-8"), right.encode("utf-8"))


def _basic_matches(header: str, settings: Settings) -> bool:
    if not settings.password:
        return False
    try:
        decoded = base64.b64decode(header.split(" ", 1)[1].strip(), validate=True)
        username, separator, password = decoded.decode("utf-8").partition(":")
    except (IndexError, binascii.Error, UnicodeDecodeError):
        return False
    if not separator:
        return False
    # Les deux comparaisons sont evaluees pour ne pas court-circuiter sur le nom.
    return _equal(username, settings.username) & _equal(password, settings.password)


def _bearer_matches(header: str, settings: Settings) -> bool:
    if not settings.api_token:
        return False
    token = header.split(" ", 1)[1].strip() if " " in header else ""
    return bool(token) and _equal(token, settings.api_token)


def authorize(request: Request) -> None:
    """Refuse la requete si elle ne porte pas d'identifiant valide."""

    settings: Settings = request.app.state.settings
    if settings.allow_anonymous:
        return

    header = request.headers.get("authorization", "")
    scheme = header.split(" ", 1)[0].lower() if header else ""

    if scheme == "basic" and _basic_matches(header, settings):
        return
    if scheme == "bearer" and _bearer_matches(header, settings):
        return

    raise HTTPException(
        status_code=status.HTTP_401_UNAUTHORIZED,
        detail="Authentification requise.",
        headers=_UNAUTHORIZED_HEADERS,
    )
