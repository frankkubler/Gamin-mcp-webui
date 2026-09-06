"""Point d'entree : ``python -m app``.

Lit l'hote et le port dans l'environnement (``WEBUI_HOST``, ``WEBUI_PORT``) puis
demarre uvicorn sur la fabrique d'application, de sorte que la configuration
soit validee au demarrage et qu'une erreur de reglage arrete le processus.
"""

from __future__ import annotations

import os
import sys

import uvicorn

from .config import ConfigError, load_settings


def main() -> int:
    try:
        load_settings()
    except ConfigError as error:
        print(f"Configuration invalide : {error}", file=sys.stderr)
        return 2

    uvicorn.run(
        "app.main:create_app",
        factory=True,
        host=os.environ.get("WEBUI_HOST", "0.0.0.0"),  # noqa: S104 - service conteneurise
        port=int(os.environ.get("WEBUI_PORT", "8080")),
        proxy_headers=True,
        forwarded_allow_ips=os.environ.get("WEBUI_FORWARDED_ALLOW_IPS", "127.0.0.1"),
        log_level=os.environ.get("WEBUI_LOG_LEVEL", "info"),
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
