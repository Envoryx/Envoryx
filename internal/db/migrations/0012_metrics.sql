-- Resource history per project container: one row per container and time bucket at
-- three resolutions (60 s, 300 s, 3600 s). Rates are per second; cpu is percent of one
-- core (150 = one and a half cores).
CREATE TABLE metric_samples (
    project_id TEXT    NOT NULL,
    container  TEXT    NOT NULL,
    res        INTEGER NOT NULL,
    ts         INTEGER NOT NULL,
    cpu        REAL    NOT NULL,
    cpu_max    REAL    NOT NULL,
    mem        INTEGER NOT NULL,
    mem_max    INTEGER NOT NULL,
    net_rx     REAL    NOT NULL,
    net_tx     REAL    NOT NULL,
    blk_read   REAL    NOT NULL,
    blk_write  REAL    NOT NULL,
    samples    INTEGER NOT NULL,
    PRIMARY KEY (project_id, res, ts, container)
) WITHOUT ROWID;

-- Disk space per project, measured hourly: its volumes, its directory, its backups.
CREATE TABLE metric_sizes (
    project_id TEXT    NOT NULL,
    kind       TEXT    NOT NULL, -- volume | files | backups
    name       TEXT    NOT NULL,
    ts         INTEGER NOT NULL,
    bytes      INTEGER NOT NULL,
    PRIMARY KEY (project_id, ts, kind, name)
) WITHOUT ROWID;
