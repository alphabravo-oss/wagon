-- Before card 61 (PR 92), registry login stored the username and password in
-- the job row's env_overrides as HAULER_REGISTRY_USERNAME/PASSWORD. Null them.
-- secure_delete zeroes the bytes the UPDATE frees; applyMigrations then runs
-- VACUUM and a TRUNCATE WAL checkpoint (scrubFreedPages), so no copy is left in
-- app.db or app.db-wal.
PRAGMA secure_delete = ON;
UPDATE jobs SET env_overrides = NULL WHERE env_overrides LIKE '%HAULER_REGISTRY_PASSWORD%';
PRAGMA secure_delete = OFF;
