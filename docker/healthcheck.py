#!/usr/bin/env python3
"""Sonde de sante du conteneur : verifie les services reellement lances.

L'interface web repond sur /api/health, le serveur MCP publie son document de
metadonnees OAuth. Seuls les services demandes par RUN_SERVICES sont testes, et
la sonde ne s'authentifie nulle part : les deux points de terminaison sont
publics par construction.
"""

from __future__ import annotations

import os
import ssl
import sys
import urllib.error
import urllib.request

TIMEOUT = 4.0

# Le serveur MCP peut terminer le TLS lui-meme, avec un certificat auto-signe
# quand GARMIN_MCP_SELF_SIGNED_TLS est actif. La sonde parle a 127.0.0.1 dans le
# conteneur : verifier la chaine n'apporterait rien et ferait echouer un
# certificat auto-signe pourtant valide pour cet usage.
SANS_VERIFICATION = ssl._create_unverified_context()  # noqa: S323


def joignable(url: str) -> tuple[bool, str]:
    contexte = SANS_VERIFICATION if url.startswith("https://") else None
    try:
        with urllib.request.urlopen(url, timeout=TIMEOUT, context=contexte) as reponse:  # noqa: S310
            if reponse.status == 200:
                return True, "ok"
            return False, f"HTTP {reponse.status}"
    except urllib.error.HTTPError as erreur:
        return False, f"HTTP {erreur.code}"
    except (urllib.error.URLError, OSError, ValueError) as erreur:
        return False, str(erreur)


def port_local(adresse: str, defaut: str) -> str:
    """Rend la partie port d'un `host:port`, en tolerant une valeur absente."""

    _, _, port = adresse.rpartition(":")
    return port or defaut


def main() -> int:
    services = os.environ.get("RUN_SERVICES", "les-deux")
    cibles: list[tuple[str, str]] = []

    if services in {"les-deux", "both", "all", "webui", "interface", "web"}:
        port = os.environ.get("WEBUI_PORT", "8080")
        cibles.append(("interface web", f"http://127.0.0.1:{port}/api/health"))

    if services in {"les-deux", "both", "all", "mcp", "garmin-mcp", "serveur"}:
        port = port_local(os.environ.get("GARMIN_MCP_BIND_ADDRESS", "127.0.0.1:8180"), "8180")
        chiffre = bool(os.environ.get("GARMIN_MCP_TLS_CERT_FILE")) or os.environ.get(
            "GARMIN_MCP_SELF_SIGNED_TLS", "0"
        ) not in {"0", "", "false", "no"}
        protocole = "https" if chiffre else "http"
        cibles.append(
            (
                "serveur MCP",
                f"{protocole}://127.0.0.1:{port}/.well-known/oauth-authorization-server",
            )
        )

    echecs = []
    for nom, url in cibles:
        ok, detail = joignable(url)
        if not ok:
            echecs.append(f"{nom} : {detail}")

    if echecs:
        print("; ".join(echecs), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
