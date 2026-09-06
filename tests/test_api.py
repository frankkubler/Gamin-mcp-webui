"""Routes HTTP : authentification, filtres, export, fuites."""

from __future__ import annotations

import base64
from pathlib import Path

import pytest
from fastapi.testclient import TestClient

from app.config import ConfigError, Settings, load_settings
from app.main import create_app

USERNAME = "operateur"
PASSWORD = "mot-de-passe-de-test"
TOKEN = "jeton-de-test"

BASIC = "Basic " + base64.b64encode(f"{USERNAME}:{PASSWORD}".encode()).decode()
AUTH = {"Authorization": BASIC}


@pytest.fixture
def client(database_path: Path) -> TestClient:
    settings = Settings(
        database_path=str(database_path),
        username=USERNAME,
        password=PASSWORD,
        api_token=TOKEN,
    )
    with TestClient(create_app(settings)) as test_client:
        yield test_client


def test_sonde_publique(client: TestClient) -> None:
    response = client.get("/api/health")
    assert response.status_code == 200
    assert response.json() == {"status": "ok", "database_readable": True}


def test_acces_refuse_sans_identifiant(client: TestClient) -> None:
    response = client.get("/api/accounts")
    assert response.status_code == 401
    assert response.headers["www-authenticate"].startswith("Basic")


@pytest.mark.parametrize(
    "header",
    [
        "Basic " + base64.b64encode(b"operateur:mauvais").decode(),
        "Basic " + base64.b64encode(b"intrus:mot-de-passe-de-test").decode(),
        "Basic pas-du-base64!!",
        "Bearer mauvais-jeton",
        "Bearer ",
        "n-importe-quoi",
    ],
)
def test_identifiants_invalides(client: TestClient, header: str) -> None:
    assert client.get("/api/accounts", headers={"Authorization": header}).status_code == 401


def test_acces_par_jeton_porteur(client: TestClient) -> None:
    response = client.get("/api/accounts", headers={"Authorization": f"Bearer {TOKEN}"})
    assert response.status_code == 200


def test_liste_des_comptes(client: TestClient) -> None:
    payload = client.get("/api/accounts", headers=AUTH).json()
    assert payload["total"] == 4
    first = payload["items"][0]
    assert first["email"] == "alice@exemple.fr"
    assert first["status"] == "actif"
    assert first["last_seen_source"] == "garmin_tokens_refreshed_at"
    assert first["last_seen_label"]


def test_filtre_par_etat(client: TestClient) -> None:
    payload = client.get("/api/accounts?status=dormant", headers=AUTH).json()
    assert [item["email"] for item in payload["items"]] == ["carol@exemple.fr"]


def test_filtre_par_recherche(client: TestClient) -> None:
    payload = client.get("/api/accounts?search=BOB@", headers=AUTH).json()
    assert payload["total"] == 1
    assert payload["items"][0]["id"] == "p-idle"


def test_filtre_par_liaison_garmin(client: TestClient) -> None:
    payload = client.get("/api/accounts?linked=non", headers=AUTH).json()
    assert [item["id"] for item in payload["items"]] == ["p-new"]


def test_tri_et_pagination(client: TestClient) -> None:
    payload = client.get("/api/accounts?sort=email&order=asc&limit=2", headers=AUTH).json()
    assert [item["email"] for item in payload["items"]] == ["alice@exemple.fr", "bob@exemple.fr"]
    assert payload["total"] == 4

    page_two = client.get(
        "/api/accounts?sort=email&order=asc&limit=2&offset=2", headers=AUTH
    ).json()
    assert [item["email"] for item in page_two["items"]] == ["carol@exemple.fr", "dan@exemple.fr"]


def test_les_comptes_sans_connexion_restent_en_fin_de_tri(client: TestClient) -> None:
    for order in ("asc", "desc"):
        payload = client.get(f"/api/accounts?sort=last_seen_at&order={order}", headers=AUTH).json()
        assert payload["items"][-1]["id"] == "p-new"


def test_tri_inconnu_refuse(client: TestClient) -> None:
    assert client.get("/api/accounts?sort=n_importe_quoi", headers=AUTH).status_code == 422


def test_statistiques(client: TestClient) -> None:
    stats = client.get("/api/stats", headers=AUTH).json()
    assert stats["accounts_total"] == 4
    assert stats["garmin_linked"] == 3
    assert stats["by_status"]["jamais_connecte"] == 1


def test_detail(client: TestClient) -> None:
    detail = client.get("/api/accounts/p-active", headers=AUTH).json()
    assert detail["consents"][0]["client_name"] == "Claude Desktop"
    assert detail["token_families"][0]["client_name"] == "Claude Desktop"
    assert detail["audit_events"] == []


def test_detail_inconnu(client: TestClient) -> None:
    assert client.get("/api/accounts/inconnu", headers=AUTH).status_code == 404


def test_export_csv(client: TestClient) -> None:
    response = client.get("/api/accounts.csv", headers=AUTH)
    assert response.status_code == 200
    assert response.headers["content-type"].startswith("text/csv")
    lines = response.text.strip().splitlines()
    assert lines[0].startswith("id,email,garmin_linked,created_at,last_seen_at")
    assert len(lines) == 5
    assert lines[1].startswith("p-active,alice@exemple.fr,oui,")
    # Le compte jamais connecte sort en dernier, avec une date vide.
    assert lines[-1].startswith("p-new,dan@exemple.fr,non,")


def test_statut_du_service(client: TestClient) -> None:
    status = client.get("/api/status", headers=AUTH).json()
    assert status["database"]["readable"] is True
    assert status["database"]["schema_version"] == 2
    assert status["signals"]["mcp_token_issued_at"]


def test_aucune_donnee_sensible_dans_les_reponses(client: TestClient) -> None:
    corpus = "".join(
        client.get(path, headers=AUTH).text
        for path in ("/api/accounts", "/api/accounts/p-active", "/api/stats", "/api/status")
    )
    for forbidden in ("garmin_account_hash", "sealed", "token_hash", "code_hash", "secret_hash"):
        assert forbidden not in corpus


def test_base_absente_donne_503(tmp_path: Path) -> None:
    settings = Settings(
        database_path=str(tmp_path / "absente.db"), username=USERNAME, password=PASSWORD
    )
    with TestClient(create_app(settings)) as client:
        response = client.get("/api/accounts", headers=AUTH)
        assert response.status_code == 503
        assert "Base introuvable" in response.json()["detail"]
        assert client.get("/api/health").json()["status"] == "degraded"


def test_page_html_servie(client: TestClient) -> None:
    response = client.get("/")
    assert response.status_code == 200
    assert "Comptes garmin-mcp" in response.text


def test_masquage_active(database_path: Path) -> None:
    settings = Settings(
        database_path=str(database_path),
        username=USERNAME,
        password=PASSWORD,
        mask_emails=True,
    )
    with TestClient(create_app(settings)) as client:
        payload = client.get("/api/accounts", headers=AUTH).json()
    assert payload["items"][0]["email"] == "a***@e***.fr"


def test_acces_anonyme_explicite(database_path: Path) -> None:
    settings = Settings(database_path=str(database_path), allow_anonymous=True)
    with TestClient(create_app(settings)) as client:
        assert client.get("/api/accounts").status_code == 200


def test_refus_de_demarrer_sans_secret() -> None:
    with pytest.raises(ConfigError):
        load_settings({"WEBUI_DATABASE_PATH": "/data/garmin.db"})


def test_reglages_depuis_l_environnement() -> None:
    settings = load_settings(
        {
            "WEBUI_DATABASE_PATH": "/ailleurs/garmin.db",
            "WEBUI_PASSWORD": "secret",
            "WEBUI_ACTIVE_DAYS": "3",
            "WEBUI_IDLE_DAYS": "45",
            "WEBUI_MASK_EMAILS": "oui",
        }
    )
    assert settings.database_path == "/ailleurs/garmin.db"
    assert (settings.active_days, settings.idle_days) == (3, 45)
    assert settings.mask_emails is True


def test_seuils_incoherents_refuses() -> None:
    with pytest.raises(ConfigError):
        load_settings({"WEBUI_PASSWORD": "x", "WEBUI_ACTIVE_DAYS": "30", "WEBUI_IDLE_DAYS": "7"})
