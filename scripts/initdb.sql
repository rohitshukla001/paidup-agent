-- Runs once, when the docker compose database is first created.
-- The store tests wipe this database, so it stays apart from "paidup".
CREATE DATABASE paidup_test OWNER paidup;
