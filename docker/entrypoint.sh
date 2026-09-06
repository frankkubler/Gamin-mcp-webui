#!/usr/bin/env bash
#
# Lance le serveur MCP et l'interface web dans le meme conteneur.
#
# Les deux processus partagent /data : garmin-mcp y ecrit sa base SQLite et sa
# cle maitre, l'interface web la lit. Ils tournent sous le meme compte de
# service, sans quoi l'interface ne pourrait pas ouvrir une base en mode WAL.
#
# Le conteneur s'arrete des que l'un des deux s'arrete : un redemarrage propre
# par Docker vaut mieux qu'un conteneur « sain » dont la moitie est morte.

set -euo pipefail

UTILISATEUR="${APP_USER:-webui}"
REPERTOIRE_DONNEES="$(dirname "${GARMIN_MCP_DATABASE_PATH:-/data/garmin.db}")"
SERVICES="${RUN_SERVICES:-les-deux}"

journal() {
    printf '%s [entrypoint] %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"
}

# Prepare /data et redescend sur le compte de service.
#
# Un volume nomme herite des droits presents dans l'image, mais un bind mount
# arrive avec ceux de l'hote : demarre en root, on corrige puis on abandonne les
# privileges. Demarre deja sans privilege, on ne touche a rien et on verifie
# seulement que le repertoire est utilisable.
if [ "$(id -u)" = "0" ]; then
    mkdir -p "${REPERTOIRE_DONNEES}"
    if [ "$(stat -c '%u' "${REPERTOIRE_DONNEES}")" != "$(id -u "${UTILISATEUR}")" ]; then
        journal "appropriation de ${REPERTOIRE_DONNEES} par ${UTILISATEUR}"
        chown -R "${UTILISATEUR}:${UTILISATEUR}" "${REPERTOIRE_DONNEES}"
    fi
    chmod 0700 "${REPERTOIRE_DONNEES}"
    journal "abandon des privileges root pour ${UTILISATEUR}"
    exec setpriv --reuid "${UTILISATEUR}" --regid "${UTILISATEUR}" --init-groups --inh-caps=-all "$0" "$@"
fi

if [ ! -w "${REPERTOIRE_DONNEES}" ]; then
    journal "ERREUR : ${REPERTOIRE_DONNEES} n'est pas inscriptible par $(id -un) (uid $(id -u))."
    journal "Montez le volume avec cette appartenance, ou demarrez le conteneur en root pour qu'il la corrige."
    exit 1
fi

# TLS auto-signe pour un essai local.
#
# Le serveur d'autorisation refuse de nommer un emetteur en clair : l'URL
# publique doit etre https, sans exception ni override. En production, un
# reverse proxy termine le TLS et publie cette URL ; pour un essai sur un poste,
# ce bloc fabrique un certificat auto-signe dans /data/tls afin que le conteneur
# termine le TLS lui-meme. Le certificat existant n'est jamais remplace.
if [ "${GARMIN_MCP_SELF_SIGNED_TLS:-0}" = "1" ] && [ -z "${GARMIN_MCP_TLS_CERT_FILE:-}" ]; then
    repertoire_tls="${REPERTOIRE_DONNEES}/tls"
    certificat="${repertoire_tls}/serveur.crt"
    cle="${repertoire_tls}/serveur.key"
    nom="${GARMIN_MCP_SELF_SIGNED_HOST:-127.0.0.1}"

    if [ ! -s "${certificat}" ] || [ ! -s "${cle}" ]; then
        journal "generation d'un certificat auto-signe pour ${nom} (essai local uniquement)"
        mkdir -p "${repertoire_tls}"
        chmod 0700 "${repertoire_tls}"
        if [[ "${nom}" =~ ^[0-9.]+$ ]]; then
            autre_nom="IP:${nom}"
        else
            autre_nom="DNS:${nom}"
        fi
        openssl req -x509 -newkey rsa:2048 -sha256 -days 825 -nodes \
            -keyout "${cle}" -out "${certificat}" \
            -subj "/CN=${nom}" -addext "subjectAltName=${autre_nom},DNS:localhost,IP:127.0.0.1" \
            >/dev/null 2>&1
        chmod 0600 "${cle}" "${certificat}"
    fi

    export GARMIN_MCP_TLS_CERT_FILE="${certificat}"
    export GARMIN_MCP_TLS_KEY_FILE="${cle}"
fi

pids=()

# shellcheck disable=SC2317  # le corps n'est atteint que par le trap ci-dessous.
arreter() {
    trap - TERM INT
    journal "arret demande, extinction des services"
    for pid in "${pids[@]}"; do
        kill -TERM "${pid}" 2>/dev/null || true
    done
    wait || true
    exit 0
}
trap arreter TERM INT

demarrer_mcp() {
    journal "demarrage du serveur MCP sur ${GARMIN_MCP_BIND_ADDRESS:-127.0.0.1:8180}"
    garmin-mcp serve &
    pids+=("$!")
}

demarrer_webui() {
    journal "demarrage de l'interface web sur ${WEBUI_HOST:-0.0.0.0}:${WEBUI_PORT:-8080}"
    python -m app &
    pids+=("$!")
}

case "${SERVICES}" in
    les-deux|both|all)
        demarrer_mcp
        demarrer_webui
        ;;
    mcp|garmin-mcp|serveur)
        demarrer_mcp
        ;;
    webui|interface|web)
        demarrer_webui
        ;;
    *)
        journal "ERREUR : RUN_SERVICES=${SERVICES} inconnu (attendu : les-deux, mcp, webui)."
        exit 2
        ;;
esac

# `wait -n` rend la main des qu'un service s'arrete ; on propage son code de
# sortie apres avoir arrete l'autre, pour que Docker applique sa politique de
# redemarrage a tout le conteneur.
set +e
wait -n
code=$?
set -e
journal "un service s'est arrete (code ${code}), extinction du conteneur"
for pid in "${pids[@]}"; do
    kill -TERM "${pid}" 2>/dev/null || true
done
wait || true
exit "${code}"
