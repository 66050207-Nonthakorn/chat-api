// check-edge validates an exported CloudFront configuration locally. It never
// loads AWS credentials or makes network requests.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
)

const cachingDisabled = "4135ea2d-6df8-44a3-9df3-4b5a84be39ad"
const allViewer = "216adef6-5c7f-47e4-b989-5492eafa07d3"

type behavior struct {
	PathPattern, TargetOriginId, CachePolicyId, OriginRequestPolicyId, ViewerProtocolPolicy string
	AllowedMethods                                                                          struct{ Items []string }
}
type distribution struct {
	CacheBehaviors       struct{ Items []behavior }
	DefaultCacheBehavior behavior
	Origins              struct {
		Items []struct {
			Id                 string
			CustomOriginConfig struct {
				OriginProtocolPolicy string
				OriginReadTimeout    int
				OriginSslProtocols   struct{ Items []string }
			}
		}
	}
	CustomErrorResponses struct {
		Items []struct {
			ErrorCode, ErrorCachingMinTTL int
			ResponsePagePath              string
		}
	}
}

func main() {
	file := flag.String("file", "", "exported distribution JSON or DistributionConfig JSON")
	origin := flag.String("origin", "", "expected ALB origin ID")
	flag.Parse()
	data, err := os.ReadFile(*file)
	if err == nil {
		err = validate(data, *origin)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("Private API/auth CloudFront configuration passed offline checks.")
}

func validate(data []byte, originID string) error {
	if originID == "" {
		return fmt.Errorf("expected ALB origin ID is required")
	}
	var wrapper struct{ DistributionConfig json.RawMessage }
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return err
	}
	if len(wrapper.DistributionConfig) > 0 {
		data = wrapper.DistributionConfig
	}
	var cfg distribution
	if err := json.Unmarshal(data, &cfg); err != nil {
		return err
	}
	foundOrigin := false
	for _, origin := range cfg.Origins.Items {
		if origin.Id != originID {
			continue
		}
		foundOrigin = true
		custom := origin.CustomOriginConfig
		if custom.OriginProtocolPolicy != "https-only" || custom.OriginReadTimeout < 60 || len(custom.OriginSslProtocols.Items) != 1 || custom.OriginSslProtocols.Items[0] != "TLSv1.2" {
			return fmt.Errorf("ALB origin requires HTTPS, TLSv1.2 only, and response timeout >=60s")
		}
	}
	if !foundOrigin {
		return fmt.Errorf("ALB origin %q not found", originID)
	}
	checkBehavior := func(b behavior) error {
		if b.TargetOriginId != originID || b.CachePolicyId != cachingDisabled || b.OriginRequestPolicyId != allViewer {
			return fmt.Errorf("behavior %q requires ALB origin, Managed-CachingDisabled and Managed-AllViewer", b.PathPattern)
		}
		if b.ViewerProtocolPolicy != "https-only" && b.ViewerProtocolPolicy != "redirect-to-https" {
			return fmt.Errorf("behavior %q must require viewer HTTPS", b.PathPattern)
		}
		for _, method := range []string{"GET", "HEAD", "OPTIONS", "POST", "PUT", "PATCH", "DELETE"} {
			found := false
			for _, allowed := range b.AllowedMethods.Items {
				if allowed == method {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("behavior %q omits allowed method %s", b.PathPattern, method)
			}
		}
		return nil
	}
	for _, b := range cfg.CacheBehaviors.Items {
		pattern := strings.TrimPrefix(b.PathPattern, "/")
		if strings.HasPrefix(pattern, "api/") || strings.HasPrefix(pattern, "auth/") {
			if err := checkBehavior(b); err != nil {
				return err
			}
		}
	}
	// Check actual first-match precedence, including broad wildcard overrides.
	for _, route := range []string{"/api/v1/session", "/api/v1/ws", "/api/v1/rooms", "/api/v1/rooms/r/messages", "/api/v1/rooms/r/sync", "/api/v1/rooms/r/messages/m/image", "/auth/login", "/auth/callback", "/auth/logout"} {
		selected := cfg.DefaultCacheBehavior
		for _, b := range cfg.CacheBehaviors.Items {
			pattern := regexp.QuoteMeta(strings.TrimPrefix(b.PathPattern, "/"))
			pattern = strings.ReplaceAll(strings.ReplaceAll(pattern, `\*`, `.*`), `\?`, `.`)
			if regexp.MustCompile("^" + pattern + "$").MatchString(strings.TrimPrefix(route, "/")) {
				selected = b
				break
			}
		}
		if err := checkBehavior(selected); err != nil {
			return fmt.Errorf("%s: %w", route, err)
		}
	}
	for _, response := range cfg.CustomErrorResponses.Items {
		if response.ErrorCachingMinTTL != 0 || response.ResponsePagePath != "" {
			return fmt.Errorf("distribution-wide error %d caches or rewrites private API errors", response.ErrorCode)
		}
	}
	return nil
}
