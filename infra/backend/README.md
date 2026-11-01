# Backend AWS infrastructure

This Terraform root manages backend-owned AWS resources only. It consumes a VPC, private subnets, the shared ALB security group, the API target group, and private-subnet egress from the platform/network owner. It does not create or change networking, ingress, frontend hosting, S3, CloudFront, or Cognito resources.

## Resources

- ECR repositories with immutable image tags, push scanning, and encryption for the API and worker.
- An ECS cluster and optional Fargate API and worker services with fixed task counts, private ENIs, digest-pinned images, and rolling-deployment rollback.
- Private, encrypted RDS PostgreSQL with managed master credentials, automated backups, PostgreSQL log exports, deletion protection, and final snapshots.
- Private, encrypted, TLS-only node-based ElastiCache Valkey with an AUTH token sent through the provider's write-only argument.
- Separate execution and application roles for each service. Execution roles can pull only their service's ECR image, write to that service's log group, and read the two runtime connection secrets. Application task roles have no AWS permissions.
- Secrets Manager secret shells for `DATABASE_URL` and `VALKEY_URL`. Terraform does not create secret versions or retain their values in state.
- Short-retention CloudWatch log groups, an ECS/RDS/Valkey dashboard, and CPU/storage alarms. Alerts are visible in CloudWatch; no notification destination is created here. The Go backend does not yet export distributed traces, so this infrastructure does not claim OpenTelemetry trace coverage.

The RDS-managed master secret is separate from the application connection secret and is not readable by ECS tasks. Create a least-privilege database user for the application and set its connection URL in the runtime secret. Set the Valkey URL as `rediss://:<URL-encoded-token>@<private-endpoint>:6379/0`; the URL and ElastiCache AUTH token must match. ECS injects both secret values at task launch, so a new task deployment is needed after changing either value.

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

The root defaults `enable_ecs_services` to `false`, allowing infrastructure and ECR to be prepared before application images or runtime secret values exist. Once ECR images have been pushed by digest and both secret versions are active, enable the ECS services. Never apply the example values below: their account/network identifiers and image digests are deliberately fake, and `offline_validation` disables provider identity checks.

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
4. Apply with `enable_ecs_services=false`, provision a database application user, push immutable API/worker images to the created ECR repositories, and populate both runtime Secrets Manager secret shells outside Terraform.
5. Set the real image digests and `enable_ecs_services=true`, review the plan, then apply through the approved AWS identity. The API listens on port 8080. The ALB health check must use `/health/ready`; public ingress and TLS remain shared-stack responsibilities.

This module does not configure schema migration execution, Cognito, OTel instrumentation/collector, autoscaling, frontend hosting, or external notification delivery. The current app has no production authentication adapter; do not treat ECS provisioning as production readiness.

## Cost, replacement, retention, and cleanup

Fargate tasks, the RDS instance/storage/backups, Valkey nodes, Secrets Manager secrets, CloudWatch logs/alarms/dashboard, ECR image storage, and the external ALB/NAT/endpoints/data transfer incur costs. The fixed task/database/cache defaults mirror an unapproved estimate in [the project cost reference](../../docs/adr/references/05-aws-cost-and-cleanup.md). CloudWatch retention defaults to seven days. The dashboard and alarms have no action destination by default. NAT, ALB, public IPv4, shared endpoints, DNS, and frontend assets are outside this module but can continue to cost after backend tasks stop.

Changing engine versions, DB class/storage/AZ mode, cache node count/type, task sizes, image digest, or shared network inputs can trigger service restarts, failover, downtime, or replacement. Database passwords never appear in Terraform configuration: RDS manages its separate master secret in Secrets Manager, while the app-user `DATABASE_URL` is entered outside Terraform. Valkey's AUTH token uses the provider's write-only input and is never written into Terraform state by that resource. Still protect state and plan files; plan files can contain infrastructure values.

For cleanup, first set `enable_ecs_services=false` and apply to stop/delete ECS services; then empty both ECR repositories because `force_delete=false`. Set `postgres_deletion_protection=false` and apply that change before destroying the DB. Choose a new unique `postgres_final_snapshot_identifier`, keep `postgres_skip_final_snapshot=false`, and destroy only after exporting any required application data. The RDS final snapshot is retained outside the instance; Valkey has no snapshots and its Pub/Sub state is discarded. Secrets enter their configured recovery window after deletion. The remote state bucket and state objects remain untouched and must be retained for recovery/audit until separately approved for retirement.
