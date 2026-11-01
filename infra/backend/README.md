# Backend AWS infrastructure

This Terraform root manages backend-owned AWS resources only. It consumes a VPC, private subnets, the shared ALB security group, the API target group, and private-subnet egress from the platform/network owner. It does not create or change networking, ingress, frontend hosting, S3, CloudFront, or Cognito resources.

## Resources

- ECR repositories with immutable image tags, push scanning, and encryption for the API and worker.
- An ECS cluster and optional Fargate API and worker services with fixed task counts, private ENIs, digest-pinned images, and rolling-deployment rollback.
- Private, encrypted RDS PostgreSQL with managed master credentials, automated backups, PostgreSQL log exports, deletion protection, and final snapshots.
- Private, encrypted, TLS-only node-based ElastiCache Valkey with an AUTH token sent through the provider's write-only argument.
- Separate execution and application roles for the API and worker. Each service execution role can pull only its own ECR image and read only its own secrets. The optional migration task has a separate execution role scoped to the API image, migration log group, and `DATABASE_URL`. Application task roles have no AWS permissions.
- Secrets Manager secret shells for database, Valkey, cursor signing, OIDC client secret, and session encryption keys. Terraform does not create secret versions or retain their values in state.
- A one-off ECS migration task definition separate from the API and worker services. It uses the API image digest supplied through `migration_image_digest` and reads only `DATABASE_URL`.
- Short-retention CloudWatch log groups, an ECS/RDS/Valkey and application dashboard, metric filters and alarms for request latency/errors, socket pressure, outbox age/retries, dependency health, and database pool pressure. Alerts are visible in CloudWatch; no notification destination is created here. The Go backend does not export distributed traces, so this infrastructure does not claim OpenTelemetry trace coverage.

The RDS-managed master secret is separate from the application connection secret and is not readable by ECS tasks. Create a least-privilege database user for the application and set its connection URL in the runtime secret. Set the Valkey URL as `rediss://:<URL-encoded-token>@<private-endpoint>:6379/0`; the URL and ElastiCache AUTH token must match. Supply a random `CURSOR_HMAC_KEY` of at least 32 bytes. The session key-ring JSON maps key IDs to base64-encoded AES-256 keys. ECS injects secrets when a task starts, so a new deployment is needed after changing a value.

### Runtime configuration and secrets

The API loads browser/OIDC/session configuration, the worker loads only database and Valkey settings, and the migration task loads only `DATABASE_URL`:

| Environment value | Storage | Consumer | Required at startup | Notes |
| --- | --- | --- | --- |
| `DATABASE_URL` | Secrets Manager | API, worker, migration | Yes | Least-privilege PostgreSQL application account. |
| `VALKEY_URL` | Secrets Manager | API, worker | Yes | TLS `rediss://` URL with the matching AUTH token. |
| `CURSOR_HMAC_KEY` | Secrets Manager | API | Yes | Random key of at least 32 bytes. |
| `OIDC_CLIENT_SECRET` | Secrets Manager | API | Yes | Secret from the externally owned OIDC app client. |
| `SESSION_ENCRYPTION_KEYS` | Secrets Manager | API | Yes | JSON key ID to base64 AES-256 key map; keep old keys during rotation until old sessions expire. |
| `APP_ORIGIN` | Terraform environment | API | Yes | Exact HTTPS browser origin, no path or trailing slash. |
| `OIDC_ISSUER` | Terraform environment | API | Yes | HTTPS issuer URL supplied by the external identity owner. Cognito resources are outside this module. |
| `OIDC_CLIENT_ID` | Terraform environment | API | Yes | External OIDC app-client ID. |
| `OIDC_REDIRECT_URL` | Terraform environment | API | Yes | Must equal `APP_ORIGIN/auth/callback`. |
| `SESSION_ENCRYPTION_CURRENT_KEY_ID` | Terraform environment | API | Yes | Must name one key in `SESSION_ENCRYPTION_KEYS`. |
| `SHUTDOWN_GRACE_SECONDS` | Terraform environment | API, worker | No (default 20) | Integer from 1 through 25; Terraform injects it. ECS `stopTimeout` is set to five seconds longer. |
| `APP_ENV` | Terraform environment | API, worker | Yes in task definition | Fixed to `production`. Migration uses a dedicated database-only configuration path. |
| `HTTP_ADDR` | Terraform environment | API | No | Defaults to `:8080`; Terraform injects it for the API. |

`OIDC_CLIENT_SECRET` and `SESSION_ENCRYPTION_KEYS` are API-only secrets; the other new values are non-secret configuration. Terraform creates five secret shells, but only injects `CURSOR_HMAC_KEY`, `OIDC_CLIENT_SECRET`, and `SESSION_ENCRYPTION_KEYS` into the API. Existing secret names remain `DATABASE_URL`, `VALKEY_URL`, and `CURSOR_HMAC_KEY`. Populate all five secret versions out of band before starting ECS tasks. It does not create or configure Cognito.

The ECS task execution role is the role ECS uses to retrieve Secrets Manager values and pull images; the task role is what application code receives. Keep the database master credential out of both task roles. See [ECS secret injection requirements](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/specifying-sensitive-data-tutorial.html) and [RDS-managed master credentials](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/rds-secrets-manager.html).

## Terraform state and bootstrap

The `s3` backend is intentionally partial. Provision the state bucket separately before initializing this root so application cleanup can never remove its own state. Configure the bucket with Block Public Access, versioning, server-side encryption, and a policy that requires TLS. Restrict object access to the specific environment key and lock object; state may expose infrastructure details and must be treated as sensitive. The bucket and its KMS key, if used, are not managed or destroyed here.

Initialize with an approved bucket and a distinct key for each environment. Do not put AWS credentials in backend configuration; authenticate through the approved role/OIDC path. S3 backend configuration supports encryption, version recovery through bucket versioning, and native lock files with `use_lockfile=true` ([Terraform S3 backend documentation](https://developer.hashicorp.com/terraform/language/backend/s3)).

```powershell
terraform init `
  -backend-config="bucket=<approved-state-bucket>" `
  -backend-config="key=cloudchat/<environment>/backend/terraform.tfstate" `
  -backend-config="region=<state-bucket-region>"
```

The deployment identity needs `s3:ListBucket` restricted to the state prefix, `s3:GetObject` and `s3:PutObject` on that state's object, and `s3:GetObject`, `s3:PutObject`, and `s3:DeleteObject` on its `.tflock` object. Add only the necessary KMS key permissions if the bucket uses a customer-managed key. Keep read access limited because reading state can reveal sensitive infrastructure data. Preserve the bucket, state objects, and lock configuration when the application stack is cleaned up; remove them only through a separately approved state-retirement process after all environments are decommissioned. See the official [backend permission and lock-file requirements](https://developer.hashicorp.com/terraform/language/backend/s3#permissions-required).

The root defaults `enable_ecs_services` to `false`, allowing infrastructure and ECR to be prepared before application images or runtime secret values exist. Once ECR images have been pushed by digest and all five secret versions are active, enable the ECS services. Never apply the example values below: their account/network identifiers and image digests are deliberately fake, and `offline_validation` disables provider identity checks.

## Backend CI and image artifacts

The root `.github/workflows/backend-ci.yml` runs Go tests against PostgreSQL and Valkey service containers, race tests, `go vet`, command builds, and Docker builds for the API and worker. Actions are pinned to full commit SHAs and the workflow has read-only repository permission. It does not authenticate to AWS or publish, deploy, or apply infrastructure. Its local Docker image IDs identify the tested build contents; they are not registry references and do not imply an AWS release.

For a local clean commit, `./infra/backend/build-images.ps1` builds both Linux container targets and verifies the embedded `org.opencontainers.image.revision` label and Docker image ID. After that exact commit has passed the repository CI checks, an operator with an approved AWS identity can publish it to the immutable ECR repositories:

```powershell
./infra/backend/publish-images.ps1 `
  -Region <region> `
  -ApiRepository <account>.dkr.ecr.<region>.amazonaws.com/<api-repository> `
  -WorkerRepository <account>.dkr.ecr.<region>.amazonaws.com/<worker-repository>
```

The script tags each image with the full Git commit SHA and reports the registry `repository@sha256:<digest>` reference. ECR rejects replacing an existing tag. Use the returned digests in Terraform; never deploy a mutable tag. The API image contains `/app/migrate` for the controlled task. The CI workflow intentionally does not run this script.

## Schema-safe release and rollback

The migration executable does not run in API startup. A release operator uses this sequence so schema work is one controlled Fargate task and service tasks continue running the prior image until the migration succeeds:

1. Merge the backend commit and confirm its CI checks passed. Build and publish API/worker images once; record the full repository digests and current task definition revisions.
2. Add only backward-compatible schema changes first: new tables, nullable columns, and indexes that old and new code can both tolerate. Keep destructive renames, drops, and required-column conversions for a later contract release.
3. Set `migration_image_digest` to the new API `repository@sha256:...` while leaving `container_image_digests` at the currently deployed service images. Review and apply the task-definition-only Terraform change. It creates a one-off migration task definition; it does not change ECS services.
4. Run that exact task-definition ARN in private subnets and check the process exit code:

   ```powershell
   ./infra/backend/run-migration.ps1 `
     -Region <region> `
     -Cluster <backend-cluster> `
     -TaskDefinitionArn <migration-task-definition-arn> `
     -PrivateSubnets @(<private-subnet-a>, <private-subnet-b>) `
     -TaskSecurityGroups @(<backend-task-security-group>)
   ```

   The migration task reads only `DATABASE_URL`, writes to the API CloudWatch log group, and exits before any API or worker task definition is updated. Do not terminate a migration task just because it is taking longer than expected; inspect its task state and migration logs first.
5. After a zero exit code and migration review, update `container_image_digests` to the new API and worker digests and apply. ECS rolls tasks with the deployment circuit breaker enabled. Verify `/health/ready`, request errors and latency, socket reconnects, outbox age/retries, dependency health, database pool pressure, and committed-message recovery.
6. Keep the expanded schema compatible with both the previous and current service versions for the rollback window. In a later release, after the previous code can no longer be restored, apply contract migrations to remove deprecated schema elements.

If the migration fails, leave the running service digests unchanged and inspect task logs before retrying. If a backward-compatible migration succeeds but the new application release fails, restore the previous API/worker digests through the reviewed Terraform change; leave the additive schema in place. Do not run an automatic down migration. If a later contract migration has removed data or old schema, rollback requires a forward repair or an approved database restore, not merely restoring an old image. Use the RDS snapshot and backup procedures in the readiness and cleanup references before any destructive schema change.

ECS Fargate's Linux `stopTimeout` has a maximum of 120 seconds; this module configures `SHUTDOWN_GRACE_SECONDS` to at most 25 and sets `stopTimeout` five seconds longer, for a bounded 30-second task stop budget ([ECS task definition parameters](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/task_definition_parameters.html)). The application marks readiness unavailable before HTTP drain, sends WebSockets a going-away close, waits for ordinary requests, and has a force-close path at the deadline. The worker stops new claims, finishes or cancels active publishes, then releases any still-owned outbox leases through PostgreSQL under the same configured grace. Clients recover durable messages through the HTTP sync contract after reconnect.

GitHub recommends pinning actions to full commit SHAs as the immutable action reference ([secure use reference](https://docs.github.com/en/actions/reference/security/secure-use)). CI is defined in source, but it has not run in this workspace's GitHub environment. No AWS account, identity, shared network, CI secret shell, or live ECS/RDS/Valkey environment is configured here; no publish, deploy, apply, live rollback, dashboard ingestion, or AWS alarm evaluation is claimed or verified.

## Non-applying local validation

From this directory, run the official Terraform container with dummy credentials and a non-secret AUTH-token placeholder:

```powershell
.\plan-offline.ps1
```

The script formats the real module, then makes an ignored `.plan-only/` copy without the S3 backend declaration. It runs `init -backend=false`, `validate`, and `plan -refresh=false` against that copy because a plan cannot initialize the real S3 backend without the approved state bucket and AWS identity. The provider receives only fake credentials; no AWS API calls, remote state, or applies are part of this check. The S3 backend declaration in the real source keeps encryption and lock-file support enabled for later deployment.

Example values for `db.t4g.small`, 20 GiB gp3, a single `cache.t4g.micro` node, `ap-southeast-1`, and engine versions are price-planning placeholders, not approved sizing or region decisions. Confirm the AWS account plan, credits, quotas, supported versions, subnet/ALB inputs, TLS requirements, and measured capacity before an apply. No real credentials are used by the validation command.

## Deployment prerequisites

1. Confirm the AWS account and plan, region, remaining credits, service availability/quotas, and an approved cost window. The infrastructure readiness reference says these remain unconfirmed.
2. Have the shared network owner supply at least two private subnet IDs in different AZs in the given VPC, the shared ALB security group, an existing API target group with target type `ip` and `/health/ready`, and an egress path. This module adds only task/data security groups and never creates an ALB or route.
3. Configure remote S3 state and lock access before running a plan intended for apply. Keep state separate from application resources.
4. Apply with `enable_ecs_services=false`, provision a database application user, push immutable API/worker images to the created ECR repositories, and populate all five runtime Secrets Manager secret shells outside Terraform.
5. Apply a valid `migration_image_digest`, run the one-off migration task to completion, then set real API/worker `container_image_digests` and `enable_ecs_services=true`. Review each plan and apply through the approved AWS identity. The API listens on port 8080. The ALB health check must use `/health/ready`; public ingress and TLS remain shared-stack responsibilities.

This module does not create or configure Cognito, OTel instrumentation/collector, autoscaling, frontend hosting, or external notification delivery. The production authentication configuration is injected from the externally owned OIDC provider. ECS provisioning and local checks are not evidence of live production readiness.

## Cost, replacement, retention, and cleanup

Fargate tasks, the RDS instance/storage/backups, Valkey nodes, Secrets Manager secrets, CloudWatch logs/alarms/dashboard, ECR image storage, and the external ALB/NAT/endpoints/data transfer incur costs. The fixed task/database/cache defaults mirror an unapproved estimate in [the project cost reference](../../docs/adr/references/05-aws-cost-and-cleanup.md). CloudWatch retention defaults to seven days. The dashboard and alarms have no action destination by default. NAT, ALB, public IPv4, shared endpoints, DNS, and frontend assets are outside this module but can continue to cost after backend tasks stop.

Changing engine versions, DB class/storage/AZ mode, cache node count/type, task sizes, image digest, or shared network inputs can trigger service restarts, failover, downtime, or replacement. Database passwords never appear in Terraform configuration: RDS manages its separate master secret in Secrets Manager, while the app-user `DATABASE_URL` is entered outside Terraform. Valkey's AUTH token uses the provider's write-only input and is never written into Terraform state by that resource. Still protect state and plan files; plan files can contain infrastructure values.

For cleanup, first set `enable_ecs_services=false` and apply to stop/delete ECS services; then empty both ECR repositories because `force_delete=false`. Set `postgres_deletion_protection=false` and apply that change before destroying the DB. Choose a new unique `postgres_final_snapshot_identifier`, keep `postgres_skip_final_snapshot=false`, and destroy only after exporting any required application data. The RDS final snapshot is retained outside the instance; Valkey has no snapshots and its Pub/Sub state is discarded. Secrets enter their configured recovery window after deletion. The remote state bucket and state objects remain untouched and must be retained for recovery/audit until separately approved for retirement.
