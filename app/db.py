"""Acces en lecture seule a la base SQLite de garmin-mcp.

garmin-mcp ouvre sa base en ``journal_mode=WAL``. SQLite ne sait pas lire une
base WAL quand le processus n'a pas le droit d'ecrire le fichier ``-shm`` : un
montage strictement en lecture seule ferait donc echouer une connexion directe.
Ce module essaie d'abord la connexion directe (``mode=ro``), puis retombe sur un
instantane : la base, son WAL et son index partage sont copies dans un repertoire
temporaire ou SQLite peut rejouer le WAL. L'instantane est mis en cache pendant
``snapshot_ttl_seconds`` pour ne pas recopier a chaque requete.

Les connexions de lecture n'ecrivent rien : elles sont ouvertes en ``mode=ro`` et
forcent ``PRAGMA query_only``.

Une seule exception existe, et elle est bornee mecaniquement : la validation des
comptes. ``connect_write`` ouvre la base en ecriture, mais installe un autorisateur
SQLite qui refuse toute ecriture ailleurs que dans ``account_approvals`` et toute
modification de schema. Ce n'est pas une convention de code : c'est SQLite qui refuse,
avant d'executer, une requete qui sortirait de cette table.
"""

from __future__ import annotations

import os
import shutil
import sqlite3
import tempfile
import threading
import time
from collections.abc import Iterator
from contextlib import contextmanager
from dataclasses import dataclass
from pathlib import Path

# Suffixes des fichiers annexes d'une base WAL.
_SIDECAR_SUFFIXES = ("-wal", "-shm")


# La seule table que cette interface a le droit d'ecrire.
APPROVALS_TABLE = "account_approvals"


class DatabaseUnavailable(RuntimeError):
    """La base n'est pas lisible : absente, illisible, ou pas une base SQLite."""


class DatabaseReadOnly(RuntimeError):
    """La base est lisible mais pas inscriptible : validation impossible."""


def _authorizer(
    action: int, argument: str | None, _second: str | None, _db: str | None, _trigger: str | None
) -> int:
    """Refuse tout ce qui sortirait de la table des validations.

    L'autorisateur est appele par SQLite pendant la preparation de chaque requete,
    donc une ecriture interdite echoue avant d'etre executee, et non apres coup.
    Lire reste permis : l'ecriture a besoin de relire la ligne qu'elle vient de
    poser, et une lecture ne peut rien abimer.
    """

    if action in (sqlite3.SQLITE_INSERT, sqlite3.SQLITE_UPDATE, sqlite3.SQLITE_DELETE):
        return sqlite3.SQLITE_OK if argument == APPROVALS_TABLE else sqlite3.SQLITE_DENY
    if action in (
        sqlite3.SQLITE_CREATE_TABLE,
        sqlite3.SQLITE_CREATE_INDEX,
        sqlite3.SQLITE_CREATE_TRIGGER,
        sqlite3.SQLITE_CREATE_VIEW,
        sqlite3.SQLITE_DROP_TABLE,
        sqlite3.SQLITE_DROP_INDEX,
        sqlite3.SQLITE_DROP_TRIGGER,
        sqlite3.SQLITE_DROP_VIEW,
        sqlite3.SQLITE_ALTER_TABLE,
        sqlite3.SQLITE_ATTACH,
        sqlite3.SQLITE_DETACH,
    ):
        return sqlite3.SQLITE_DENY
    return sqlite3.SQLITE_OK


@dataclass(frozen=True)
class DatabaseInfo:
    """Ce que l'interface affiche sur la source de donnees."""

    path: str
    exists: bool
    readable: bool
    size_bytes: int | None
    modified_at: float | None
    access_mode: str  # "direct" | "snapshot" | "inconnu"
    schema_version: int | None


class Database:
    """Ouvre des connexions en lecture seule sur la base de garmin-mcp."""

    def __init__(self, path: str | os.PathLike[str], snapshot_ttl_seconds: float = 5.0):
        self.path = Path(path)
        self.snapshot_ttl_seconds = snapshot_ttl_seconds
        self._lock = threading.Lock()
        self._access_mode = "inconnu"
        self._snapshot_dir: tempfile.TemporaryDirectory[str] | None = None
        self._snapshot_taken_at = 0.0

    # -- connexions ----------------------------------------------------------

    @contextmanager
    def connect(self) -> Iterator[sqlite3.Connection]:
        """Rend une connexion en lecture seule, fermee a la sortie du bloc."""

        if not self.path.exists():
            raise DatabaseUnavailable(
                f"Base introuvable : {self.path}. Vérifiez le montage du volume "
                "de garmin-mcp et WEBUI_DATABASE_PATH."
            )

        try:
            connection = self._open(self.path)
            self._access_mode = "direct"
        except sqlite3.Error as direct_error:
            try:
                connection = self._open(self._snapshot())
                self._access_mode = "snapshot"
            except (sqlite3.Error, OSError) as snapshot_error:
                raise DatabaseUnavailable(
                    f"Impossible de lire {self.path} : {direct_error} "
                    f"(instantané également en échec : {snapshot_error})"
                ) from snapshot_error

        try:
            yield connection
        finally:
            connection.close()

    @contextmanager
    def connect_write(self) -> Iterator[sqlite3.Connection]:
        """Rend une connexion inscriptible, bornee a la table des validations.

        Elle ne passe jamais par l'instantane : ecrire dans une copie temporaire
        serait une decision perdue au prochain rafraichissement. Si la base n'est
        pas inscriptible — montage en lecture seule, droits de fichier — la
        validation n'est pas possible et l'appelant doit le dire, pas l'ignorer.
        """

        if not self.path.exists():
            raise DatabaseUnavailable(
                f"Base introuvable : {self.path}. Vérifiez le montage du volume "
                "de garmin-mcp et WEBUI_DATABASE_PATH."
            )

        uri = f"file:{self.path.as_posix()}?mode=rw"
        try:
            connection = sqlite3.connect(uri, uri=True, timeout=10.0, isolation_level=None)
        except sqlite3.Error as error:
            raise DatabaseReadOnly(
                f"Impossible d'ouvrir {self.path} en écriture ({error}). La validation "
                "des comptes demande un volume monté en écriture pour l'interface."
            ) from error

        connection.row_factory = sqlite3.Row
        try:
            connection.execute("PRAGMA foreign_keys = ON")
            connection.execute("PRAGMA busy_timeout = 10000")
            connection.set_authorizer(_authorizer)
            yield connection
        except sqlite3.OperationalError as error:
            # SQLite refuse l'ecriture d'une base dont le fichier ou le repertoire
            # n'est pas inscriptible ; le message le dit mieux qu'une 500.
            if "readonly" in str(error).lower():
                raise DatabaseReadOnly(
                    f"{self.path} est ouverte en lecture seule ({error})."
                ) from error
            raise
        finally:
            connection.close()

    def _open(self, path: Path) -> sqlite3.Connection:
        uri = f"file:{path.as_posix()}?mode=ro"
        connection = sqlite3.connect(uri, uri=True, timeout=5.0)
        connection.row_factory = sqlite3.Row
        try:
            connection.execute("PRAGMA query_only = ON")
            # Sonde : une base WAL non lisible echoue ici et pas a l'ouverture.
            connection.execute("SELECT count(*) FROM sqlite_master").fetchone()
        except sqlite3.Error:
            connection.close()
            raise
        return connection

    # -- instantane ----------------------------------------------------------

    def _snapshot(self) -> Path:
        """Copie la base (et son WAL) dans un repertoire temporaire inscriptible."""

        with self._lock:
            fresh = (
                self._snapshot_dir is not None
                and (time.monotonic() - self._snapshot_taken_at) < self.snapshot_ttl_seconds
            )
            if fresh and self._snapshot_dir is not None:
                return Path(self._snapshot_dir.name) / self.path.name

            new_dir = tempfile.TemporaryDirectory(prefix="garmin-mcp-webui-")
            target = Path(new_dir.name) / self.path.name
            shutil.copyfile(self.path, target)
            for suffix in _SIDECAR_SUFFIXES:
                sidecar = self.path.with_name(self.path.name + suffix)
                if sidecar.exists():
                    shutil.copyfile(sidecar, target.with_name(target.name + suffix))

            previous = self._snapshot_dir
            self._snapshot_dir = new_dir
            self._snapshot_taken_at = time.monotonic()
            if previous is not None:
                previous.cleanup()
            return target

    def close(self) -> None:
        """Libere l'instantane courant, s'il y en a un."""

        with self._lock:
            if self._snapshot_dir is not None:
                self._snapshot_dir.cleanup()
                self._snapshot_dir = None

    # -- diagnostic ----------------------------------------------------------

    def info(self) -> DatabaseInfo:
        """Decrit la source de donnees, sans lever d'exception."""

        exists = self.path.exists()
        size: int | None = None
        modified: float | None = None
        if exists:
            stat = self.path.stat()
            size = stat.st_size
            modified = stat.st_mtime

        schema_version: int | None = None
        readable = False
        try:
            with self.connect() as connection:
                readable = True
                row = connection.execute(
                    "SELECT MAX(version) AS version FROM schema_migrations"
                ).fetchone()
                if row is not None and row["version"] is not None:
                    schema_version = int(row["version"])
        except (DatabaseUnavailable, sqlite3.Error):
            pass

        return DatabaseInfo(
            path=str(self.path),
            exists=exists,
            readable=readable,
            size_bytes=size,
            modified_at=modified,
            access_mode=self._access_mode,
            schema_version=schema_version,
        )
