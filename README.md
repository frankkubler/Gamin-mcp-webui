# Gamin-mcp-webui

Interface web de consultation, en lecture seule, des **comptes créés sur un serveur
[garmin-mcp](https://github.com/tamcore/garmin-mcp) distant et de leur dernière connexion**.

Le projet amont est un serveur MCP écrit en Go qui, en mode `remote`, authentifie plusieurs
comptes via OAuth 2.1 et stocke leur état dans une base SQLite (`database-path`, en général
`/data/garmin.db`). Il n'expose aucune page ni aucune API d'administration : impossible de
savoir, depuis le serveur lui-même, qui a créé un compte ni quand ce compte s'est connecté
pour la dernière fois. Cette interface comble ce manque sans modifier garmin-mcp : elle lit
sa base et rend le résultat dans un navigateur.

![Capture de l'interface : tuiles d'indicateurs et tableau des comptes avec leur dernière connexion](docs/interface.png)

```
┌──────────────┐   écrit    ┌─────────────┐   lit (RO)   ┌────────────────┐
│  garmin-mcp  │ ─────────► │ garmin.db   │ ◄─────────── │ Gamin-mcp-webui│
│  (Go, OAuth) │            │  (SQLite)   │              │ (FastAPI + UI) │
└──────────────┘            └─────────────┘              └────────────────┘
```

## Ce que l'interface affiche

| Colonne              | Origine |
| -------------------- | ------- |
| Compte               | `principals.email_normalized` — l'e-mail est l'identifiant de connexion, la seule donnée en clair du compte. |
| Garmin lié           | présence de `principals.garmin_account_hash`. |
| Compte créé le       | `principals.created_at`. |
| Dernière connexion   | dérivée (voir ci-dessous). |
| Signal retenu        | le fait horodaté qui porte la date affichée. |
| Clients / familles   | `consents` non révoqués et `token_families` actives. |

Le détail d'un compte ajoute la liste complète des signaux, les clients OAuth autorisés avec
leurs portées, les familles de jetons et, si la table est alimentée, les événements d'audit.

## Comment la « dernière connexion » est calculée

**Le schéma de garmin-mcp n'a pas de colonne « dernière connexion ».** Il stocke des faits
horodatés ; l'interface prend le plus récent d'entre eux et indique lequel a gagné.

| Signal | Source | Ce qu'il prouve |
| ------ | ------ | --------------- |
| Rafraîchissement des jetons Garmin | `garmin_token_sets.updated_at` | usage réel : les jetons Garmin sont réécrits à chaque rafraîchissement. |
| Émission d'un jeton MCP | `max(mcp_tokens.issued_at)` via `token_families` | le client MCP a obtenu un jeton d'accès ou l'a fait tourner. |
| Autorisation dans le navigateur | `max(auth_codes.created_at)` | passage complet par la page de login. |
| Consentement accordé | `max(consents.granted_at)` | consentement donné à un client MCP. |
| Événement d'audit | `max(audit_events.occurred_at)` | table présente dans le schéma, sans écrivain dans les versions 0.0.x. |
| Mise à jour du compte | `principals.updated_at` | repli, retenu seulement s'il est postérieur à `created_at`. |

Un compte dont aucun signal ne dépasse sa date de création est classé **jamais connecté** —
`updated_at` vaut alors `created_at` et ne prouve aucune connexion.

États dérivés (seuils configurables) : **actif** ≤ 7 j, **inactif** ≤ 30 j, **dormant** au-delà,
**jamais connecté** sans aucun signal.

### Limites à connaître

- **Rétention.** La tâche de nettoyage de garmin-mcp supprime les codes et les jetons expirés.
  Un compte inactif depuis longtemps peut donc voir sa dernière connexion « reculer » sur un
  signal plus ancien mais persistant (consentement, jetons Garmin). Les consentements, eux, ne
  sont jamais supprimés par le nettoyage.
- **Rien n'est instrumenté côté requête.** garmin-mcp n'écrit pas d'horodatage de dernier appel
  d'outil ; la granularité est donc celle de l'émission des jetons, pas celle de chaque requête
  MCP. Pour une vraie mesure d'usage, il faudrait un correctif amont alimentant `audit_events` —
  cette interface l'affichera automatiquement si la table se remplit.
- **Identité Garmin.** Le nom Garmin du compte est chiffré dans la base (`garmin_identity_sealed`)
  et n'est déchiffrable qu'avec la clé maître du serveur. L'interface ne la demande pas et
  n'affiche donc que l'e-mail.

## Sécurité

- **Lecture seule, sans exception.** Chaque connexion SQLite est ouverte en `mode=ro` avec
  `PRAGMA query_only`, et un test vérifie qu'un `DELETE` échoue.
- **Aucun secret ne sort.** Les colonnes `*_hash` (empreintes) et `*_sealed` (enveloppes
  chiffrées) ne sont jamais sélectionnées ni renvoyées ; un test parcourt les réponses de
  l'API pour s'en assurer.
- **Authentification obligatoire.** Le service refuse de démarrer sans `WEBUI_PASSWORD` ni
  `WEBUI_API_TOKEN`, sauf `WEBUI_ALLOW_ANONYMOUS=1` assumé explicitement. Les comparaisons de
  secrets passent par `secrets.compare_digest`.
- **Données personnelles.** L'interface affiche des adresses e-mail : exposez-la sur une adresse
  privée, derrière un reverse proxy en TLS, et activez `WEBUI_MASK_EMAILS=1` si un affichage
  partiel suffit. La page envoie `noindex, nofollow`.

## Démarrage rapide

### Avec Docker Compose, à côté de garmin-mcp

```bash
cp .env.example .env      # renseignez au minimum WEBUI_PASSWORD
docker compose up -d      # l'interface écoute sur 127.0.0.1:8080
```

Le volume de garmin-mcp est monté en lecture seule (`/data:ro`). garmin-mcp ouvrant sa base en
mode WAL, SQLite ne peut pas lire directement une base WAL sans droit d'écriture sur le fichier
`-shm` : l'interface bascule alors automatiquement sur une copie temporaire de la base et de son
WAL (voir `app/db.py`), rafraîchie toutes les `WEBUI_SNAPSHOT_TTL_SECONDS`. Le mode réellement
utilisé est affiché sous le titre et exposé par `/api/status`.

### En local, sans conteneur

```bash
python -m venv .venv && source .venv/bin/activate
pip install -r requirements-dev.txt

# Pour essayer sans base de production :
python scripts/demo_database.py /tmp/demo.db

WEBUI_DATABASE_PATH=/tmp/demo.db WEBUI_PASSWORD=demo python -m app
# http://127.0.0.1:8080 — identifiants : admin / demo
```

## Configuration

Toutes les variables sont facultatives sauf le secret d'accès.

| Variable | Défaut | Rôle |
| -------- | ------ | ---- |
| `WEBUI_DATABASE_PATH` | `/data/garmin.db` | base SQLite de garmin-mcp (sa clé `database-path`). |
| `WEBUI_USERNAME` | `admin` | identifiant HTTP Basic. |
| `WEBUI_PASSWORD` | — | mot de passe HTTP Basic. Obligatoire (voir `WEBUI_ALLOW_ANONYMOUS`). |
| `WEBUI_API_TOKEN` | — | jeton porteur pour l'API JSON (`Authorization: Bearer …`). |
| `WEBUI_ALLOW_ANONYMOUS` | `0` | ouvre l'interface sans authentification. À réserver à un proxy qui authentifie déjà. |
| `WEBUI_MASK_EMAILS` | `0` | masque les e-mails (`a***@e***.fr`) dans l'interface et les exports. |
| `WEBUI_ACTIVE_DAYS` | `7` | seuil « actif », en jours. |
| `WEBUI_IDLE_DAYS` | `30` | seuil « inactif », en jours. Doit dépasser `WEBUI_ACTIVE_DAYS`. |
| `WEBUI_SNAPSHOT_TTL_SECONDS` | `5` | durée de validité de la copie quand la lecture directe échoue. |
| `WEBUI_TITLE` | `Comptes garmin-mcp` | titre affiché. |
| `WEBUI_HOST` / `WEBUI_PORT` | `0.0.0.0` / `8080` | écoute HTTP. |
| `WEBUI_LOG_LEVEL` | `info` | niveau de log uvicorn. |

## API

Toutes les routes `/api` sauf `/api/health` exigent une authentification.

| Route | Description |
| ----- | ----------- |
| `GET /` | l'interface. |
| `GET /api/health` | sonde publique : `{"status": "ok", "database_readable": true}`. |
| `GET /api/status` | chemin, taille et mode d'accès de la base, seuils, libellés des signaux. |
| `GET /api/stats` | compteurs agrégés (total, liés à Garmin, actifs, vus sous 24 h…). |
| `GET /api/accounts` | liste paginée. Paramètres : `search`, `status`, `linked`, `sort`, `order`, `limit`, `offset`. |
| `GET /api/accounts.csv` | même liste au format CSV, mêmes filtres. |
| `GET /api/accounts/{id}` | détail d'un compte : signaux, consentements, familles de jetons, audit. |
| `GET /api/docs` | documentation OpenAPI générée. |

```bash
curl -u admin:motdepasse 'http://127.0.0.1:8080/api/accounts?status=dormant'
curl -H "Authorization: Bearer $WEBUI_API_TOKEN" http://127.0.0.1:8080/api/stats
```

## Développement

```bash
pip install -r requirements-dev.txt
ruff check . && ruff format --check .
pytest -q
```

Les tests construisent une base SQLite au schéma de garmin-mcp (`tests/schema.sql`) peuplée de
cas limites : compte actif, inactif, dormant, jamais connecté, compte dont les jetons ont été
purgés. `tests/schema.sql` reproduit les tables et colonnes lues par `app/queries.py` ; si le
schéma amont évolue, c'est le fichier à mettre à jour — un test échouera alors immédiatement.

## Compatibilité

Vérifié contre le schéma de garmin-mcp après les migrations `0001_initial` et
`0002_oauth_contract` (serveur `0.0.x`). L'interface ne dépend que des tables `principals`,
`garmin_token_sets`, `consents`, `oauth_clients`, `auth_codes`, `token_families`, `mcp_tokens`
et `audit_events`, et ne lit aucune colonne chiffrée.

## Licence

MIT, comme le projet amont. garmin-mcp est un projet tiers ; ce dépôt n'en est ni un fork ni un
produit officiel.
