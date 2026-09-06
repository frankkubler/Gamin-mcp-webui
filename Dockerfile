# syntax=docker/dockerfile:1
#
# Image unique : le serveur MCP garmin-mcp (Go, dossier garmin-mcp/) et
# l'interface web (Python) tournent cote a cote, sur la meme base SQLite.
#
# La base et la cle maitre vivent dans /data, qui doit etre un volume : le
# reste du systeme de fichiers peut rester en lecture seule.

# --- Etape 1 : compilation du serveur MCP --------------------------------------
# garmin-mcp utilise modernc.org/sqlite, une implementation SQLite en Go pur :
# CGO_ENABLED=0 donne donc un binaire statique sans dependance systeme.
FROM golang:1.27-trixie AS serveur

ARG GARMIN_MCP_VERSION=fork-webui
ARG GARMIN_MCP_COMMIT=inconnu

WORKDIR /src

COPY garmin-mcp/go.mod garmin-mcp/go.sum ./
RUN go mod download

COPY garmin-mcp/ ./
RUN CGO_ENABLED=0 GOFLAGS=-buildvcs=false go build \
        -trimpath \
        -ldflags="-s -w -X main.version=${GARMIN_MCP_VERSION} -X main.commit=${GARMIN_MCP_COMMIT}" \
        -o /out/garmin-mcp ./cmd/garmin-mcp \
    && /out/garmin-mcp version

# --- Etape 2 : image finale ----------------------------------------------------
FROM python:3.12-slim-trixie

LABEL org.opencontainers.image.title="gamin-mcp-webui" \
      org.opencontainers.image.description="Serveur garmin-mcp et interface web des comptes dans une seule image" \
      org.opencontainers.image.source="https://github.com/frankkubler/Gamin-mcp-webui" \
      org.opencontainers.image.licenses="MIT"

# Emplacements partages par les deux processus, et service(s) a lancer :
# RUN_SERVICES vaut "les-deux", "mcp" ou "webui".
ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    GARMIN_MCP_STATE_DIR=/data \
    GARMIN_MCP_DATABASE_PATH=/data/garmin.db \
    GARMIN_MCP_MASTER_KEY_FILE=/data/keys/key-v1.json \
    GARMIN_MCP_TRANSPORT=streamable-http \
    GARMIN_MCP_BIND_ADDRESS=0.0.0.0:8180 \
    WEBUI_DATABASE_PATH=/data/garmin.db \
    WEBUI_HOST=0.0.0.0 \
    WEBUI_PORT=8080 \
    RUN_SERVICES=les-deux

WORKDIR /srv

# openssl sert au certificat auto-signe optionnel (GARMIN_MCP_SELF_SIGNED_TLS),
# util-linux fournit setpriv, qui abandonne les privileges root de l'entrypoint.
RUN apt-get update \
    && apt-get install -y --no-install-recommends openssl util-linux \
    && rm -rf /var/lib/apt/lists/*

COPY requirements.txt ./
RUN pip install --no-cache-dir -r requirements.txt

COPY app ./app
COPY docker/entrypoint.sh docker/healthcheck.py /usr/local/bin/
COPY --from=serveur /out/garmin-mcp /usr/local/bin/garmin-mcp

# Le compte de service. /data lui appartient dans l'image, de sorte qu'un volume
# nomme cree par Docker herite de cette appartenance.
RUN useradd --system --uid 10001 --user-group --home /srv webui \
    && mkdir -p /data \
    && chown 10001:10001 /data \
    && chmod 0700 /data \
    && chmod +x /usr/local/bin/entrypoint.sh

VOLUME ["/data"]

# 8080 : interface web. 8180 : endpoint MCP et pages de login OAuth.
EXPOSE 8080 8180

HEALTHCHECK --interval=30s --timeout=6s --start-period=15s --retries=3 \
    CMD ["python", "/usr/local/bin/healthcheck.py"]

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
