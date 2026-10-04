"""Le script de test SMTP : codes de retour et messages, sans rien envoyer.

Le script vit dans `docker/` parce qu'il est copié dans l'image et lancé par
`docker exec`, pas importé par l'application. Ces tests le chargent donc par son
chemin, et couvrent ce qu'un essai contre un vrai serveur ne montre pas : les
refus de configuration, qui doivent sortir par un message et un code, jamais par
une trace d'exception.
"""

from __future__ import annotations

import importlib.util
import os
import stat
from collections.abc import Iterator
from pathlib import Path
from types import ModuleType

import pytest

SCRIPT = Path(__file__).resolve().parent.parent / "docker" / "smtp-test.py"

REGLAGES = {
    "GARMIN_MCP_SMTP_HOST": "smtp.exemple.fr",
    "GARMIN_MCP_SMTP_PORT": "587",
    "GARMIN_MCP_SMTP_USER": "envoi@exemple.fr",
    "GARMIN_MCP_SMTP_TO": "exploitant@exemple.fr",
    "GARMIN_MCP_SMTP_TLS": "starttls",
}


def charger() -> ModuleType:
    """Importe le script par son chemin : son nom porte un tiret."""

    specification = importlib.util.spec_from_file_location("smtp_test", SCRIPT)
    assert specification is not None and specification.loader is not None
    module = importlib.util.module_from_spec(specification)
    specification.loader.exec_module(module)
    return module


@pytest.fixture
def script() -> ModuleType:
    return charger()


@pytest.fixture
def secret(tmp_path: Path) -> Path:
    chemin = tmp_path / "smtp"
    chemin.write_text("abcdefghijklmnop\n", encoding="utf8")
    chemin.chmod(0o600)
    return chemin


@pytest.fixture
def environnement(monkeypatch: pytest.MonkeyPatch, secret: Path) -> Iterator[None]:
    for nom in os.environ:
        if nom.startswith("GARMIN_MCP_SMTP"):
            monkeypatch.delenv(nom, raising=False)
    for nom, valeur in REGLAGES.items():
        monkeypatch.setenv(nom, valeur)
    monkeypatch.setenv("GARMIN_MCP_SMTP_SECRET_FILE", str(secret))
    yield


def test_configuration_complete_est_rapportee(
    script: ModuleType, environnement: None, secret: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    assert script.main(["--sans-envoi"]) == 0

    sortie = capsys.readouterr().out
    for attendu in ["smtp.exemple.fr:587", "envoi@exemple.fr", "16 caracteres", "0600"]:
        assert attendu in sortie
    # Le secret lui-meme n'est jamais affiche : seule sa longueur l'est.
    assert "abcdefghijklmnop" not in sortie


def test_un_mode_tls_inconnu_est_un_refus_pas_une_trace(
    script: ModuleType,
    environnement: None,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    # Le mutant que ce test attrape : valider le mode dans connexion(), hors du
    # bloc qui rattrape Manquant, fait remonter l'exception jusqu'a Python, qui
    # imprime une trace. La premiere version du script faisait exactement cela.
    monkeypatch.setenv("GARMIN_MCP_SMTP_TLS", "cleartext")

    assert script.main([]) == 2

    erreur = capsys.readouterr().err
    assert "cleartext" in erreur
    assert "starttls" in erreur
    assert "Traceback" not in erreur


@pytest.mark.parametrize("absente", ["GARMIN_MCP_SMTP_HOST", "GARMIN_MCP_SMTP_USER"])
def test_un_reglage_absent_est_nomme(
    script: ModuleType,
    environnement: None,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    absente: str,
) -> None:
    monkeypatch.delenv(absente)

    assert script.main(["--sans-envoi"]) == 2
    assert absente in capsys.readouterr().err


def test_un_secret_absent_parle_du_montage(
    script: ModuleType,
    environnement: None,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    # L'erreur qui a coute une soiree a un deploiement reel : le fichier est sur
    # la machine hote, et le chemin est celui du conteneur.
    monkeypatch.setenv("GARMIN_MCP_SMTP_SECRET_FILE", "/run/secrets/smtp")

    assert script.main(["--sans-envoi"]) == 2

    erreur = capsys.readouterr().err
    assert "/run/secrets/smtp" in erreur
    assert "montage" in erreur


def test_un_secret_vide_est_refuse(script: ModuleType, environnement: None, secret: Path) -> None:
    secret.write_text("   \n", encoding="utf8")

    assert script.main(["--sans-envoi"]) == 2


def test_un_secret_trop_ouvert_est_signale(
    script: ModuleType, environnement: None, secret: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    secret.chmod(0o644)

    # Le script le signale sans refuser : c'est le serveur qui tranche au
    # demarrage, et ce script sert justement a diagnostiquer ce refus.
    assert script.main(["--sans-envoi"]) == 0
    assert "ATTENTION" in capsys.readouterr().err
    assert stat.S_IMODE(secret.stat().st_mode) == 0o644


def test_le_destinataire_en_argument_remplace_la_liste(
    script: ModuleType, environnement: None, capsys: pytest.CaptureFixture[str]
) -> None:
    assert script.main(["--sans-envoi", "essai@exemple.fr"]) == 0

    sortie = capsys.readouterr().out
    assert "essai@exemple.fr" in sortie
    assert "exploitant@exemple.fr" not in sortie
