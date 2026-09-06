# Image de l'interface web. Elle ne contient pas de donnees : la base de
# garmin-mcp est montee au demarrage, en lecture seule.
FROM python:3.12-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    WEBUI_DATABASE_PATH=/data/garmin.db \
    WEBUI_HOST=0.0.0.0 \
    WEBUI_PORT=8080

WORKDIR /srv

COPY requirements.txt ./
RUN pip install --no-cache-dir -r requirements.txt

COPY app ./app

# Utilisateur sans privilege : l'interface n'a besoin que de lire /data.
RUN useradd --system --uid 10001 --home /srv webui
USER 10001

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD python -c "import urllib.request,sys; \
sys.exit(0 if urllib.request.urlopen('http://127.0.0.1:8080/api/health', timeout=4).status == 200 else 1)"

CMD ["python", "-m", "app"]
