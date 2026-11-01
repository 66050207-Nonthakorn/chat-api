# 06: Define backend AWS runtime infrastructure

**What to build:** The backend has a repeatable Terraform plan for its private compute and data services, with least-privilege access and explicit inputs for shared networking owned outside this backend scope.

**Blocked by:** 01: Run a local backend foundation.

**Status:** ready-for-agent

- [ ] Terraform defines backend ECS/Fargate API and worker services, ECR repositories, RDS PostgreSQL, ElastiCache, task roles, Secrets Manager access, and backend telemetry.
- [ ] Database and cache remain private; task roles grant only required access and credentials load at runtime.
- [ ] Network, ingress, and frontend resources are consumed as explicit shared inputs rather than duplicated or changed here.
- [ ] `terraform fmt`, `validate`, and a plan with non-secret example inputs succeed without applying resources.
- [ ] The plan documents resources that incur cost, replacement/data-retention risks, and cleanup behavior.
- [ ] Live apply remains gated on approved AWS account/plan, region, credits, and shared network inputs.
