# Temporary AWS demo cost estimate

Estimate checked: 2026-10-25 (Asia/Bangkok). Currency: USD, before tax and currency conversion. This is a planning estimate, not an AWS quote, measured capacity claim, or authorization to provision resources. Notion has not been changed.

## Scope accepted in chat

Prioritize the Cloud Computing rubric: a small, working AWS-hosted chat demonstrates compute, object storage, external database state, networking, security, deployment, recovery, and observability. Implement sign-in, seeded users/conversations, text sending, history, and reconnect recovery. Product features such as group-management screens, typing, presence, read receipts, and edit/delete remain later work.

Preserve the selected architecture: Go modular monolith, HTTP durable commands, WebSocket live events, PostgreSQL messages/sessions/outbox, a separate Go publisher, Valkey Pub/Sub, Cognito, ECS/Fargate, RDS, node-based ElastiCache, private tasks, ALB, S3/CloudFront, Secrets Manager, CloudWatch/OpenTelemetry, Terraform, and GitHub Actions rolling releases. Deployment is on demand; local development is the default.

## Estimation assumptions

- Provisional region: Singapore, ap-southeast-1. Region selection is not yet approved. Reprice if another region is chosen.
- Linux x86 Fargate on-demand, no Spot or reserved commitments. Two API tasks, each 0.5 vCPU/1 GiB, and one worker task at 0.25 vCPU/0.5 GiB. Instrumentation must fit these allocations; validate in local tests and resize if needed.
- RDS PostgreSQL db.t4g.small with 20 GB gp3 storage. Development uses Single-AZ; the reliability demo uses traditional Multi-AZ with one standby. The quoted Multi-AZ compute/storage rates already include the standby; do not multiply them again.
- ElastiCache Valkey cache.t4g.micro: one development node; primary plus one replica for the reliability profile. These are proposed estimate sizes, not proven throughput.
- ALB across two AZs. Allow one average LCU; additional LCU consumption is usage-based.
- One zonal NAT gateway in development; two, one per AZ, for reliability testing. No paid interface endpoints assumed. Count two ALB public IPv4 addresses plus one per NAT gateway. Actual ALB address count can grow with capacity.
- No other simultaneously running project environment. Charge runtime from provisioning through deletion, not only the time spent presenting. Examples below include setup/rehearsal time within their hour budgets.
- Around ten human/test identities, no SMS, low-volume email, no paid Cognito advanced security add-ons. Many connections can reuse test identities; do not run a login storm against Cognito.
- At most about 1 GB of logs and 1 GB of retained images/static files, sampled traces, around ten custom metrics, a small dashboard/alarm set, and modest transfer. These are cost-planning limits to verify, not automatic account caps.
- Existing domain/subdomain available. Domain purchase is excluded. Use a non-exportable ACM public certificate for HTTPS; confirm the selected certificate configuration before provisioning.
- Existing eligible GitHub runner allowance assumed; paid GitHub plans/runner overage excluded. Generate test load locally initially; cloud load-generator compute would be additional.

## Verified unit prices

Rates were retrieved directly from AWS public regional catalogs; no AWS account was accessed. Filtered catalog evidence is in `tmp/aws-cost-rates.json` and `tmp/aws-nat-rate-evidence.json`.

| Item | Singapore rate | Source |
|---|---:|---|
| Fargate Linux x86 vCPU | $0.05056/vCPU-hour | [ECS catalog](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonECS/current/ap-southeast-1/index.json) |
| Fargate Linux x86 memory | $0.00553/GiB-hour | ECS catalog |
| RDS db.t4g.small, Single-AZ | $0.051/hour | [RDS catalog](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonRDS/current/ap-southeast-1/index.json) |
| RDS db.t4g.small, Multi-AZ | $0.102/hour | RDS catalog |
| RDS gp3, Single-AZ / Multi-AZ | $0.138 / $0.276 per GB-month | RDS catalog |
| ElastiCache Valkey cache.t4g.micro | $0.0192/node-hour | [ElastiCache catalog](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonElastiCache/current/ap-southeast-1/index.json) |
| ALB | $0.0252/hour + $0.008/LCU-hour | [ELB catalog](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AWSELB/current/ap-southeast-1/index.json) |
| Zonal NAT gateway | $0.059/hour + $0.059/GB processed | [EC2 regional catalog](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonEC2/current/ap-southeast-1/index.csv) |
| Public IPv4 | $0.005/address-hour | [VPC pricing](https://aws.amazon.com/vpc/pricing/) |

Storage conversions below use 730 hours/month for planning. Catalog publication dates: ECS 2026-10-19, RDS 2026-10-20, ElastiCache 2026-10-14, ELB 2026-09-11. Refresh before the actual run.

## Hourly running cost

| Component | Development | Reliability demo |
|---|---:|---:|
| Two API tasks combined | $0.06162 | $0.06162 |
| One publisher task | $0.015405 | $0.015405 |
| RDS compute | $0.05100 | $0.10200 |
| RDS 20 GB storage, prorated | $0.00378 | $0.00756 |
| Valkey node(s) | $0.01920 | $0.03840 |
| NAT gateway(s), hourly only | $0.05900 | $0.11800 |
| Public IPv4 addresses | $0.01500 | $0.02000 |
| ALB plus one LCU allowance | $0.03320 | $0.03320 |
| **Core total/hour** | **$0.25821** | **$0.39619** |

Example API calculation: 2 x (0.5 x $0.05056 + 1 x $0.00553) = $0.06162/hour.

These totals exclude the usage-dependent items budgeted below. Fargate allocation is not a promise to achieve the stretch workload. Database CPU-credit charges can apply under sustained burst load; RDS currently lists $0.075 per vCPU-hour of surplus credits. See [RDS pricing](https://aws.amazon.com/rds/postgresql/pricing/).

## Session budget

| Scenario | Development hours | Demo-profile hours | Core calculation | Suggested total allowance |
|---|---:|---:|---:|---:|
| Small number of integration sessions, rehearsal, class | 24 | 12 | $6.20 + $4.75 = **$10.95** | **$20-30** |
| Extra debugging/rehearsals | 48 | 24 | $12.39 + $9.51 = **$21.90** | **$35-50** |

Hour budgets include resource creation and cleanup time. Do not run both profiles together unless those overlapping hours are added separately.

Suggested allowance for the first scenario, beyond its $10.95 core:

- $5-8 for low-volume CloudWatch logs, metrics, alarms, dashboard, traces, and queries. This is an allowance, not a precisely priced telemetry bill. Keep log retention short and avoid user/message IDs as custom metric dimensions.
- $2-4 for S3 requests/storage, ECR, Secrets Manager, DNS, retained snapshots, NAT data processing, cross-AZ traffic, and small amounts of email/other requests. Large snapshots or high fan-out traffic can exceed it.
- $1-2 for temporary rollout tasks, short scaling experiments, extra LCU use, and billing granularity.
- Remaining headroom for provisioning delays and usage variance. Tax and payment-provider conversion remain additional.

Recommend reserving **$50 before tax** for the complete project, while aiming to spend **$20-30**. This reserve is not a hard spending cap. No promotional credits are subtracted from this estimate. Cognito Lite/Essentials direct sign-in and CloudFront have published recurring free allowances, but account-wide usage and eligibility must be checked; do not assume the whole stack is free.

If left running continuously, the core reliability profile is about **$66.56/week** or **$289.22 per 730-hour month**, before telemetry, transfer, and other extras. Runtime discipline matters more than shaving a few cents off a short demo.

## Execution and cleanup policy

1. Test locally first. Provision development for a bounded session; record start time and a cleanup deadline.
2. Rehearse the exact class sequence before the presentation. The live sequence should prioritize cross-replica chat, worker/API recovery, rolling deployment, and dashboards. Collect scaling/failover evidence beforehand where timing is unpredictable.
3. Cap task scaling before tests. A brief increase from two to four API tasks adds about $0.06162 per hour of overlap; telemetry and transfer can cost more than this compute increase.
4. Use a free S3 gateway endpoint where applicable to avoid NAT processing for S3/ECR image layers. Verify all Cognito/ECR/logging/secrets routes; do not assume endpoints eliminate every outbound requirement.
5. Export test results and approved data. Destroy Fargate services, ALB, NAT gateways, ElastiCache, and RDS when the session ends. Release unused Elastic IPs. A stopped RDS database still incurs storage charges; stopping tasks does not delete networking charges.
6. Review retained S3 objects, ECR images, log groups, snapshots, secrets, DNS zones, and any paid endpoints. Document exactly what remains and its ongoing cost.
7. Use actual/forecast AWS Budget alerts plus bounded capacity and session deadlines. AWS Budget alerts are delayed and do not provide a guaranteed hard cap.

## Additional official pricing references

- [CloudWatch](https://aws.amazon.com/cloudwatch/pricing/)
- [Secrets Manager](https://aws.amazon.com/secrets-manager/pricing/) - $0.40 per secret-month, prorated, plus API calls in the published example.
- [Cognito](https://aws.amazon.com/cognito/pricing/) - direct/social sign-in free allowance depends on tier; email/SMS and extras are separate.
- [CloudFront pay-as-you-go](https://aws.amazon.com/cloudfront/pricing/pay-as-you-go/) - recurring free transfer/request allowance is shared with account usage.
- [AWS Budgets behavior](https://docs.aws.amazon.com/cost-management/latest/userguide/budgets-managing-costs.html)

## Open before provisioning

Confirm region, available domain, lab-account restrictions/credits, class date, the accepted spending reserve, and actual instance/version availability. No infrastructure has been provisioned. The prior feature decisions remain design records, with implementation deferred beyond the minimum infrastructure demo where appropriate.
