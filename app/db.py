"""Acces en lecture seule a la base SQLite de garmin-mcp.

garmin-mcp ouvre sa base en ``journal_mode=WAL``. SQLite ne sait pas lire une
base WAL quand le processus n'a pas le droit d'ecrire le fichier ``-shm`` : un
montage strictement en lecture seule ferait donc echouer une connexion directe.
Ce module essaie d'abord la connexion directe (``mode=ro``), puis retombe sur un
instantane : la base, son WAL et son index partage sont copies dans un repertoire
temporaire ou SQLite peut rejouer le WAL. L'instantane est mis en cache pendant
``snapshot_ttl_seconds`` pour ne pas recopier a chaque requete.

Aucune ecriture n'est possible : chaque connexion est ouverte en ``mode=ro`` et
force ``PRAGMA query_only``.
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


class DatabaseUnavailable(RuntimeError):
    """La base n'est pas lisible : absente, illisible, ou pas une base SQLite."""


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
