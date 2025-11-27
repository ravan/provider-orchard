/*
Copyright 2025 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package orchardclient

import (
	"fmt"
	"net/http"
)

// OrchardConfig holds configuration for the Orchard client
type OrchardConfig struct {
	BaseURL string
	Token   string
}

// OrchardClient wraps the generated Orchard API client with authentication
type OrchardClient struct {
	*ClientWithResponses
	config OrchardConfig
}

// NewOrchardClient creates a new Orchard client with Bearer token authentication
func NewOrchardClient(config OrchardConfig) (*OrchardClient, error) {
	if config.BaseURL == "" {
		config.BaseURL = "http://localhost:6120"
	}

	httpClient := &http.Client{
		Transport: &authTransport{
			token: config.Token,
			base:  http.DefaultTransport,
		},
	}

	client, err := NewClientWithResponses(
		config.BaseURL,
		WithHTTPClient(httpClient),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create orchard client: %w", err)
	}

	return &OrchardClient{
		ClientWithResponses: client,
		config:              config,
	}, nil
}

// authTransport adds Bearer token authentication to HTTP requests
type authTransport struct {
	token string
	base  http.RoundTripper
}

// RoundTrip implements http.RoundTripper interface
func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.token != "" {
		req.Header.Set("Authorization", "Bearer "+t.token)
	}
	return t.base.RoundTrip(req)
}

// GetBaseURL returns the configured base URL
func (c *OrchardClient) GetBaseURL() string {
	return c.config.BaseURL
}
