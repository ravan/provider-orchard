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

package ssh

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"

	"github.com/pkg/errors"
	"nhooyr.io/websocket"
)

// dialWebSocket establishes a WebSocket connection to Orchard's port-forward endpoint.
// This creates a tunnel to the specified port on the VM through the Orchard controller.
func dialWebSocket(ctx context.Context, config TunnelConfig) (net.Conn, error) {
	// Build the WebSocket URL from the base URL
	wsURL, err := buildWebSocketURL(config)
	if err != nil {
		return nil, errors.Wrap(err, "failed to build WebSocket URL")
	}

	// Set up headers with authentication
	headers := http.Header{}
	// User-Agent is required - Orchard server uses it to determine WebSocket message type.
	// Without User-Agent, server expects MessageText but we send MessageBinary, causing data corruption.
	headers.Set("User-Agent", "provider-orchard/1.0")
	if config.BearerToken != "" {
		headers.Set("Authorization", "Bearer "+config.BearerToken)
	}

	// Dial WebSocket
	ws, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: headers,
	})
	if err != nil {
		if resp != nil {
			switch resp.StatusCode {
			case http.StatusNotFound:
				return nil, errors.Wrapf(ErrVMNotReady, "VM %q not found (HTTP %d)", config.VMName, resp.StatusCode)
			case http.StatusServiceUnavailable:
				return nil, errors.Wrapf(ErrConnectionFailed, "failed to connect to VM %q worker (HTTP %d)", config.VMName, resp.StatusCode)
			case http.StatusBadRequest:
				return nil, errors.Wrapf(ErrConnectionFailed, "invalid port specified (HTTP %d)", resp.StatusCode)
			default:
				return nil, errors.Wrapf(ErrConnectionFailed, "WebSocket dial failed with status %d: %v", resp.StatusCode, err)
			}
		}
		return nil, errors.Wrap(ErrConnectionFailed, err.Error())
	}

	// Disable read limit since we're tunneling SSH data which can be large
	ws.SetReadLimit(-1)

	return newWSNetConn(ctx, ws), nil
}

// buildWebSocketURL constructs the WebSocket URL for the port-forward endpoint.
func buildWebSocketURL(config TunnelConfig) (string, error) {
	baseURL, err := url.Parse(config.OrchardBaseURL)
	if err != nil {
		return "", fmt.Errorf("invalid Orchard base URL %q: %w", config.OrchardBaseURL, err)
	}

	// Convert http:// to ws:// or https:// to wss://
	scheme := "ws"
	if baseURL.Scheme == "https" {
		scheme = "wss"
	}

	// Construct port-forward URL: {base}/vms/{name}/port-forward?port=22&wait=30
	// The base URL should include the API version path (e.g., http://localhost:6120/v1)
	wsURL := fmt.Sprintf("%s://%s%s/vms/%s/port-forward?port=%d",
		scheme,
		baseURL.Host,
		baseURL.Path,
		url.PathEscape(config.VMName),
		config.SSHPort,
	)

	if config.WaitSeconds > 0 {
		wsURL += fmt.Sprintf("&wait=%d", config.WaitSeconds)
	}

	return wsURL, nil
}
