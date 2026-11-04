# Shared CloudFront integration

The distribution and ALB are owned by the shared network/frontend stack. This directory prepares their required configuration without creating or changing AWS resources.

`distribution.example.json` is a **fragment**, not a deployable distribution. Merge its origin/behaviors into the owning stack, using the actual origin ID and DNS name. Keep the frontend's S3 default behavior separately. Place the private behaviors before broad wildcard routes. The fragment uses AWS's [Managed-CachingDisabled](https://docs.aws.amazon.com/AmazonCloudFront/latest/DeveloperGuide/using-managed-cache-policies.html#managed-cache-policy-caching-disabled) and [Managed-AllViewer](https://docs.aws.amazon.com/AmazonCloudFront/latest/DeveloperGuide/using-managed-origin-request-policies.html#managed-origin-request-policy-all-viewer) policies: zero caching and forwarding of session cookies, query strings, Origin, CSRF, Content-Type, viewer Host and WebSocket handshake headers.

The ALB origin must use HTTPS and TLSv1.2. Its certificate must match the forwarded viewer Host or the configured origin hostname as required by CloudFront. Set the CloudFront origin response timeout to 60 seconds and ALB idle timeout to 60 seconds. Upload reads and request work use a 45-second budget; API HTTP keep-alive is 65 seconds. A deployment can interrupt an upload at the 20-second drain limit; clients retry the same client_message_id. Reads use a 10-second bound and may fail midstream; retry the image GET only.

Allow all seven methods at CloudFront for these private behaviors; the Go router still enforces endpoint methods and CSRF. Preserve WebSocket forwarding on `/api/v1/ws`. Do not use distribution-wide SPA error-page rewrites or cache API errors: rewrite frontend routes only, for example in a viewer-request function scoped to the static behavior. Keep the media bucket private and route image GETs through the authorized API.

Validate an already exported distribution configuration locally (either the AWS CLI wrapper or bare DistributionConfig JSON is accepted):

```powershell
go run ./cmd/check-edge -file path/to/distribution.json -origin actual-alb-origin-id
```

Check the supplied fragment with:

```powershell
go run ./cmd/check-edge -file infra/shared-edge/distribution.example.json -origin chat-alb
```

The checker makes no AWS calls. It verifies the recipe's managed policy IDs, private route precedence, methods, TLS, response timeout, and error handling. It deliberately rejects custom policy IDs because their contents cannot be verified from this one JSON export. Passing these checks does not prove that the distribution was deployed, its certificate is valid, the ALB timeout is correct, or that login/upload/WebSockets work through the real edge. Those remain release checks for the shared stack owner.
