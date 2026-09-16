// Package infisicalmod is an Infisical-backed config.Source. It reads secrets
// from an Infisical project/environment over the REST API and exposes each one
// as a config bind key, so a service can inject secrets through the same
// config.Bind mechanism as file and environment sources. It has no external
// dependencies beyond net/http.
//
//	func main() {
//	    app := bosun.New()
//	    config.Add(infisicalmod.Source{
//	        ProjectID:   os.Getenv("INFISICAL_PROJECT_ID"),
//	        Environment: "prod",
//	        Token:       os.Getenv("INFISICAL_TOKEN"),
//	    })
//	    log.Fatal(app.Run(":8080"))
//	}
package infisicalmod

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/bluebeard63/bosun/config"
)

// Source loads secrets from Infisical. It implements config.Source.
type Source struct {
	// APIURL is the Infisical base URL; defaults to https://app.infisical.com.
	APIURL string
	// ProjectID is the Infisical workspace id.
	ProjectID string
	// Environment is the environment slug, e.g. "dev" or "prod".
	Environment string
	// SecretPath is the folder path; defaults to "/".
	SecretPath string
	// Token is a machine-identity or service token used as a bearer token.
	Token string
	// HTTP is the client to use; defaults to http.DefaultClient.
	HTTP *http.Client
}

var _ config.Source = Source{}

// Load fetches the secrets and returns them keyed by secret name. A secret whose
// value is a JSON object or array is passed through as raw JSON (so it can bind
// to a struct); every other value is exposed as a JSON string.
func (s Source) Load(ctx context.Context) (map[string]json.RawMessage, error) {
	base := s.APIURL
	if base == "" {
		base = "https://app.infisical.com"
	}
	secretPath := s.SecretPath
	if secretPath == "" {
		secretPath = "/"
	}
	q := url.Values{
		"workspaceId": {s.ProjectID},
		"environment": {s.Environment},
		"secretPath":  {secretPath},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/api/v3/secrets/raw?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)

	client := s.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("infisical: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}

	var payload struct {
		Secrets []struct {
			SecretKey   string `json:"secretKey"`
			SecretValue string `json:"secretValue"`
		} `json:"secrets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}

	out := make(map[string]json.RawMessage, len(payload.Secrets))
	for _, sec := range payload.Secrets {
		out[sec.SecretKey] = rawValue(sec.SecretValue)
	}
	return out, nil
}

// rawValue keeps JSON objects and arrays as-is and wraps everything else as a
// JSON string, so both structured and opaque secrets bind correctly.
func rawValue(v string) json.RawMessage {
	t := strings.TrimSpace(v)
	if (strings.HasPrefix(t, "{") && strings.HasSuffix(t, "}")) ||
		(strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]")) {
		if json.Valid([]byte(t)) {
			return json.RawMessage(t)
		}
	}
	b, _ := json.Marshal(v)
	return b
}
