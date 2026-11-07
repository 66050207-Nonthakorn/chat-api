package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type oidcMetadata struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
}

func discoverOIDC(ctx context.Context, client *http.Client, issuer string, requireHTTPS bool) (oidcMetadata, error) {
	metadataURL := strings.TrimSuffix(issuer, "/") + "/.well-known/openid-configuration"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return oidcMetadata{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return oidcMetadata{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return oidcMetadata{}, fmt.Errorf("issuer discovery returned HTTP %d", response.StatusCode)
	}
	var metadata oidcMetadata
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&metadata); err != nil {
		return oidcMetadata{}, err
	}
	if metadata.Issuer != issuer {
		return oidcMetadata{}, errors.New("discovery issuer does not match OIDC_ISSUER")
	}
	for name, endpoint := range map[string]string{
		"authorization_endpoint": metadata.AuthorizationEndpoint,
		"token_endpoint":         metadata.TokenEndpoint,
		"jwks_uri":               metadata.JWKSURI,
	} {
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Scheme != "https" && !(parsed.Scheme == "http" && !requireHTTPS) {
			return oidcMetadata{}, fmt.Errorf("discovery %s must be an absolute %s URL", name, map[bool]string{true: "https", false: "http or https"}[requireHTTPS])
		}
	}
	return metadata, nil
}
