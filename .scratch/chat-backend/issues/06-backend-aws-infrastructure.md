# 06: Define backend AWS runtime infrastructure

**Type:** task

**What to build:** The backend has a repeatable Terraform plan for its private compute and data services, with least-privilege access and explicit inputs for shared networking owned outside this backend scope.

**Blocked by:** 01: Run a local backend foundation.

**Status:** resolved

- [x] Terraform defines backend ECS/Fargate API and worker services, ECR repositories, RDS PostgreSQL, ElastiCache, task roles, Secrets Manager access, and backend telemetry.
- [x] Terraform state uses encrypted remote storage, locking, and restricted access; application cleanup does not destroy the state backend.
- [x] Database and cache remain private; task roles grant only required access and credentials load at runtime.
- [x] Network, ingress, and frontend resources are consumed as explicit shared inputs rather than duplicated or changed here.
- [x] `terraform fmt`, `validate`, and a plan with non-secret example inputs succeed without applying resources.
- [x] The plan documents resources that incur cost, replacement/data-retention risks, and cleanup behavior.
- [x] Live apply remains gated on approved AWS account/plan, region, credits, and shared network inputs.

## Answer

Implemented in `a837685` and merged into `integration` at `a837685`.

Verification: Terraform formatting, validation, and an offline placeholder plan passed (`40 to add, 0 to change, 0 to destroy`) using fake credentials and a temporary copy with the external S3 backend block removed. No AWS resources were applied; live behavior remains unverified.
