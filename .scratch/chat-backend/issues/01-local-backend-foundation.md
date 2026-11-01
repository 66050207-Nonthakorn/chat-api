# 01: Run a local backend foundation

**What to build:** A developer can start the backend locally, apply its schema and repeatable demo seed, and check process and dependency health without AWS.

**Blocked by:** None (can start immediately).

**Status:** ready-for-agent

- [ ] The Go API, worker, and migration commands build as separate entry points.
- [ ] Docker Compose provides PostgreSQL and Valkey for local development.
- [ ] Migrations apply from an empty database; demo users and rooms can be seeded repeatedly without duplicates.
- [ ] `/health/live` reports process liveness, and `/health/ready` reports dependency/schema readiness without exposing internals.
- [ ] Local test identity is explicitly disabled outside local mode.
- [ ] Startup, migration, seed, and shutdown commands are documented.
