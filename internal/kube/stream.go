package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/net/websocket"
)

func (c *Client) Authorization() (string, error) {
	token := c.token
	if c.tokenFile != "" {
		b, e := os.ReadFile(c.tokenFile)
		if e != nil {
			return "", e
		}
		token = strings.TrimSpace(string(b))
	}
	if token != "" {
		return "Bearer " + token, nil
	}
	return "", nil
}
func (c *Client) Raw(ctx context.Context, path string) ([]byte, error) {
	req, e := http.NewRequestWithContext(ctx, "GET", c.Server+path, nil)
	if e != nil {
		return nil, e
	}
	auth, e := c.Authorization()
	if e != nil {
		return nil, e
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, e := c.HTTP.Do(req)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, &APIError{Code: resp.StatusCode, Method: "GET", Path: path}
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// ExecTrue verifies the real apiserver -> konnectivity -> kubelet exec path.
func (c *Client) ExecTrue(ctx context.Context, namespace, pod string) error {
	server, e := url.Parse(c.Server)
	if e != nil {
		return e
	}
	server.Scheme = "wss"
	server.Path = "/api/v1/namespaces/" + namespace + "/pods/" + pod + "/exec"
	server.RawQuery = "command=true&stdout=true&stderr=true"
	config, e := websocket.NewConfig(server.String(), c.Server)
	if e != nil {
		return e
	}
	config.Protocol = []string{"v5.channel.k8s.io"}
	config.TlsConfig = c.HTTP.Transport.(*http.Transport).TLSClientConfig.Clone()
	auth, e := c.Authorization()
	if e != nil {
		return e
	}
	if auth != "" {
		config.Header.Set("Authorization", auth)
	}
	conn, e := config.DialContext(ctx)
	if e != nil {
		return fmt.Errorf("kubelet exec connection: %w", e)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(20 * time.Second))
	for {
		var frame []byte
		if e = websocket.Message.Receive(conn, &frame); e != nil {
			return fmt.Errorf("kubelet exec stream: %w", e)
		}
		if len(frame) > 1 && frame[0] == 3 {
			var status struct {
				Status string `json:"status"`
			}
			if e = json.Unmarshal(frame[1:], &status); e != nil {
				return e
			}
			if status.Status != "Success" {
				return fmt.Errorf("remote exec command failed")
			}
			return nil
		}
	}
}
