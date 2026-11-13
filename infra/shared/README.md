# Shared AWS demo infrastructure

This Terraform root supplies the network, HTTPS load balancer, Cognito user pool,
private frontend bucket, and CloudFront distribution required by the backend
root in `../`. It is a temporary, single-NAT demonstration environment; the
NAT gateway is a single point of failure. The load balancer accepts HTTPS only
from the CloudFront origin-facing managed prefix list. Private API/auth/health
behaviors disable caching and forward viewer headers and cookies.

The network has separate private compute and data subnets in two Availability
Zones. `private_subnet_ids` supplies the compute subnets, which route outbound
traffic through NAT; `data_subnet_ids` supplies isolated data subnets with no
default internet route. Pass both outputs to the backend root.

The state must be stored in a separate, access-restricted S3 backend with
versioning, encryption, public access blocked, TLS enforced, and lock files
(`use_lockfile` is enabled). Cognito's generated client secret is in this state;
treat state and saved Terraform plans as secrets. Certificates are external to
this root: request an ACM certificate covering both app and origin hostnames in
the deployment region and another in us-east-1 for CloudFront, and validate both
before applying. Point the origin hostname at the ALB and the app hostname at
the CloudFront distribution using DNS-only CNAME records. The ACM validation
records must stay in DNS for renewal.

The frontend bucket and distribution are created here, but publishing frontend
files, configuring backend runtime secrets, running migrations, enrolling user
identities, and starting ECS services are separate steps described in `../README.md`.
New users must confirm their email address with a Cognito verification code before
signing in.
CloudFront must be allowed on the AWS account; new accounts can require AWS
Support verification. Never assume a successful plan means the services were
created or the application is working.

This root does not automatically shut down. NAT, ALB, backend services, and
retained storage can incur charges even when nobody is using the app. Teardown
requires the backend stack to be cleaned up first, followed by this shared root;
S3 objects and ECR images can prevent deletion and must be handled explicitly.
Retain the separately managed state bucket until all stacks are fully destroyed.
