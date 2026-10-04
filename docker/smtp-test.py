#!/usr/bin/env python3
"""Teste l'envoi SMTP du deploiement, avec les reglages du serveur.

Le serveur n'envoie un message que lorsqu'un compte se met en attente de
validation, ce qui est malcommode a declencher pour verifier une configuration.
Ce script rejoue le meme echange — meme hote, meme port, meme mode TLS, meme
compte, meme secret — et dit ce qui a repondu.

    docker exec garmin-mcp python3 /usr/local/bin/smtp-test.py

Il ne prend aucun reglage : tout vient des variables GARMIN_MCP_SMTP_* du
conteneur, de sorte qu'un essai reussi porte sur la configuration reelle et non
sur une saisie faite pour l'occasion. Le secret n'est jamais affiche ; seule sa
longueur l'est, ce qui suffit a reperer une valeur tronquee.

Ce qu'il ne couvre pas : les controles de permission que le serveur applique au
fichier de secret. Ceux-la sont deja prouves par le fait que le conteneur
demarre — un fichier refuse par internal/securefile fait echouer le demarrage.
Ce script lit donc le fichier simplement, et se contente de rapporter son mode
et son proprietaire.
"""

from __future__ import annotations

import argparse
import contextlib
import os
import smtplib
import ssl
import stat
import sys
from email.message import EmailMessage

DELAI = 20.0

# Les deux modes que le serveur accepte. Il n'y a pas de mode en clair : le
# serveur amont refuse d'envoyer un identifiant sur une liaison non chiffree.
MODE_IMPLICITE = "implicit"
MODE_STARTTLS = "starttls"


class Manquant(Exception):
    """Un reglage indispensable est absent."""


def reglage(nom: str, defaut: str | None = None) -> str:
    valeur = os.environ.get(nom, "").strip()
    if valeur:
        return valeur
    if defaut is not None:
        return defaut
    raise Manquant(
        f"{nom} n'est pas renseignee. Le serveur n'enverrait rien non plus : "
        "voir « Être prévenu par e-mail » dans le README."
    )


def destinataires(remplacement: str | None) -> list[str]:
    brut = remplacement if remplacement else reglage("GARMIN_MCP_SMTP_TO")
    adresses = [part.strip() for part in brut.split(",") if part.strip()]
    if not adresses:
        raise Manquant("la liste des destinataires est vide")
    return adresses


def secret(chemin: str) -> tuple[str, int, int, int]:
    """Lit le secret et rend sa description, sans jamais montrer son contenu."""

    try:
        etat = os.stat(chemin)
    except OSError as erreur:
        raise Manquant(
            f"{chemin} est illisible : {erreur}. Le chemin est celui du conteneur : "
            "en Docker, le fichier n'y arrive que par un montage."
        ) from erreur

    with open(chemin, encoding="utf8") as fichier:
        contenu = fichier.read().strip()
    if not contenu:
        raise Manquant(f"{chemin} est vide")
    return contenu, stat.S_IMODE(etat.st_mode), etat.st_uid, etat.st_gid


def mode_tls() -> str:
    """Rend le mode TLS, en refusant toute autre valeur que les deux connues.

    La verification est ici, avec les autres reglages, et non au moment de la
    connexion : une valeur inattendue est une erreur de configuration et doit
    sortir par le meme chemin que les autres — un message et un code de retour,
    pas une trace d'exception. C'est ce que la premiere version faisait mal.
    """

    mode = reglage("GARMIN_MCP_SMTP_TLS", MODE_STARTTLS)
    if mode not in {MODE_STARTTLS, MODE_IMPLICITE}:
        raise Manquant(
            f"GARMIN_MCP_SMTP_TLS vaut {mode!r} ; attendu {MODE_STARTTLS!r} ou "
            f"{MODE_IMPLICITE!r}. Il n'y a pas de mode en clair."
        )
    return mode


def connexion(hote: str, port: int, mode: str) -> smtplib.SMTP:
    """Ouvre la liaison chiffree, en verifiant le certificat du serveur."""

    contexte = ssl.create_default_context()
    if mode == MODE_IMPLICITE:
        return smtplib.SMTP_SSL(hote, port, context=contexte, timeout=DELAI)
    liaison = smtplib.SMTP(hote, port, timeout=DELAI)
    liaison.ehlo()
    liaison.starttls(context=contexte)
    liaison.ehlo()
    return liaison


def message(expediteur: str, cibles: list[str]) -> EmailMessage:
    courriel = EmailMessage()
    courriel["From"] = expediteur
    courriel["To"] = ", ".join(cibles)
    courriel["Subject"] = "garmin-mcp : test d'envoi"
    courriel.set_content(
        "Si vous lisez ceci, la configuration SMTP de ce deploiement garmin-mcp "
        "fonctionne : les comptes en attente de validation seront annonces a "
        "cette adresse.\n"
    )
    return courriel


def main(argv: list[str] | None = None) -> int:
    analyseur = argparse.ArgumentParser(
        description="Envoie un message de test avec les reglages SMTP du deploiement."
    )
    analyseur.add_argument(
        "destinataire",
        nargs="?",
        help="adresse de test, au lieu de GARMIN_MCP_SMTP_TO (utile pour ne pas "
        "ecrire a toute la liste)",
    )
    analyseur.add_argument(
        "--sans-envoi",
        action="store_true",
        help="verifie les reglages et le secret, sans ouvrir de connexion",
    )
    arguments = analyseur.parse_args(argv)

    try:
        hote = reglage("GARMIN_MCP_SMTP_HOST")
        port = int(reglage("GARMIN_MCP_SMTP_PORT", "587"))
        compte = reglage("GARMIN_MCP_SMTP_USER")
        expediteur = os.environ.get("GARMIN_MCP_SMTP_FROM", "").strip() or compte
        cibles = destinataires(arguments.destinataire)
        mode = mode_tls()
        chemin = reglage("GARMIN_MCP_SMTP_SECRET_FILE", "/run/secrets/smtp")
        mot_de_passe, mode_fichier, proprietaire, groupe = secret(chemin)
    except Manquant as erreur:
        print(f"configuration : {erreur}", file=sys.stderr)
        return 2
    except ValueError as erreur:
        print(f"GARMIN_MCP_SMTP_PORT : {erreur}", file=sys.stderr)
        return 2

    print(f"serveur  {hote}:{port} en {mode}")
    print(f"compte   {compte}")
    print(f"de       {expediteur}")
    print(f"a        {', '.join(cibles)}")
    print(
        f"secret   {chemin} : {len(mot_de_passe)} caracteres, "
        f"mode {mode_fichier:04o}, proprietaire {proprietaire}:{groupe}"
    )
    if mode_fichier & 0o077:
        print(
            "         ATTENTION : lisible au-dela de son proprietaire. Le serveur "
            "refuserait de demarrer avec ce mode.",
            file=sys.stderr,
        )

    if arguments.sans_envoi:
        print("--sans-envoi : rien n'a ete envoye.")
        return 0

    try:
        liaison = connexion(hote, port, mode)
    except (OSError, smtplib.SMTPException, ssl.SSLError) as erreur:
        print(f"connexion : {erreur}", file=sys.stderr)
        return 1

    try:
        liaison.login(compte, mot_de_passe)
        liaison.send_message(message(expediteur, cibles))
    except smtplib.SMTPAuthenticationError as erreur:
        print(
            f"authentification refusee par {hote} : {erreur}. Avec Gmail, il faut un "
            "mot de passe d'application (validation en deux etapes active), saisi "
            "sans ses espaces.",
            file=sys.stderr,
        )
        return 1
    except (OSError, smtplib.SMTPException) as erreur:
        print(f"envoi : {erreur}", file=sys.stderr)
        return 1
    finally:
        # Un QUIT qui echoue apres un envoi accepte ne change rien au resultat.
        with contextlib.suppress(OSError, smtplib.SMTPException):
            liaison.quit()

    print(f"OK — {hote} a accepte le message pour {', '.join(cibles)}.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
