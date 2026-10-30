// Package kube provides the small Kubernetes API surface used by enrollment.
package kube

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

type Object = map[string]any
type Client struct {
	Server    string
	CA        []byte
	HTTP      *http.Client
	token     string
	tokenFile string
}
type APIError struct {
	Code         int
	Method, Path string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("Kubernetes %s %s: HTTP %d", e.Method, e.Path, e.Code)
}
func Code(err error, n int) bool { e, ok := err.(*APIError); return ok && e.Code == n }

func New(ctx context.Context, path string) (*Client, error) {
	c := &Client{}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if path == "" {
		c.Server = "https://" + os.Getenv("KUBERNETES_SERVICE_HOST") + ":" + os.Getenv("KUBERNETES_SERVICE_PORT")
		var err error
		c.CA, err = os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/ca.crt")
		if err != nil {
			return nil, err
		}
		c.tokenFile = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	} else {
		cmd := exec.CommandContext(ctx, "kubectl", "--kubeconfig", path, "config", "view", "--raw", "--minify", "--flatten", "-o", "json")
		b, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("read kubeconfig through kubectl: %w", err)
		}
		var v struct {
			Clusters []struct {
				Cluster struct {
					Server   string `json:"server"`
					CA       string `json:"certificate-authority-data"`
					Insecure bool   `json:"insecure-skip-tls-verify"`
				} `json:"cluster"`
			} `json:"clusters"`
			Users []struct {
				User struct {
					Token string `json:"token"`
					Cert  string `json:"client-certificate-data"`
					Key   string `json:"client-key-data"`
					Exec  any    `json:"exec"`
				} `json:"user"`
			} `json:"users"`
		}
		if err = json.Unmarshal(b, &v); err != nil {
			return nil, err
		}
		if len(v.Clusters) != 1 || len(v.Users) != 1 || v.Clusters[0].Cluster.Insecure || v.Users[0].User.Exec != nil {
			return nil, fmt.Errorf("one TLS-verified, certificate/token kubeconfig context required")
		}
		c.Server = v.Clusters[0].Cluster.Server
		c.CA, err = base64.StdEncoding.DecodeString(v.Clusters[0].Cluster.CA)
		if err != nil {
			return nil, err
		}
		c.token = v.Users[0].User.Token
		if v.Users[0].User.Cert != "" {
			cert, e := base64.StdEncoding.DecodeString(v.Users[0].User.Cert)
			if e != nil {
				return nil, e
			}
			key, e := base64.StdEncoding.DecodeString(v.Users[0].User.Key)
			if e != nil {
				return nil, e
			}
			pair, e := tls.X509KeyPair(cert, key)
			if e != nil {
				return nil, e
			}
			tlsConfig.Certificates = []tls.Certificate{pair}
		}
	}
	if !strings.HasPrefix(c.Server, "https://") {
		return nil, fmt.Errorf("HTTPS Kubernetes API required")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(c.CA) {
		return nil, fmt.Errorf("invalid cluster CA")
	}
	tlsConfig.RootCAs = pool
	c.HTTP = &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfig}, Timeout: 30 * time.Second}
	return c, nil
}
func (c *Client) Do(ctx context.Context, method, path string, body any) (Object, error) {
	var input io.Reader
	if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			return nil, e
		}
		input = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.Server, "/")+path, input)
	if err != nil {
		return nil, err
	}
	token := c.token
	if c.tokenFile != "" {
		b, e := os.ReadFile(c.tokenFile)
		if e != nil {
			return nil, e
		}
		token = strings.TrimSpace(string(b))
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	if method == "PATCH" {
		req.Header.Set("Content-Type", "application/merge-patch+json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &APIError{Code: resp.StatusCode, Method: method, Path: path}
	}
	var out Object
	err = json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&out)
	if err == io.EOF {
		err = nil
	}
	return out, err
}
func (c *Client) Get(ctx context.Context, path string) (Object, error) {
	return c.Do(ctx, "GET", path, nil)
}
func (c *Client) Apply(ctx context.Context, collection, name string, obj Object) error {
	_, err := c.Get(ctx, collection+"/"+name)
	if Code(err, 404) {
		_, err = c.Do(ctx, "POST", collection, obj)
		return err
	}
	if err != nil {
		return err
	}
	_, err = c.Do(ctx, "PATCH", collection+"/"+name, obj)
	return err
}
func (c *Client) Delete(ctx context.Context, path string) error {
	_, err := c.Do(ctx, "DELETE", path, Object{"propagationPolicy": "Foreground"})
	if Code(err, 404) {
		return nil
	}
	return err
}
func Map(o Object, key string) Object  { v, _ := o[key].(map[string]any); return v }
func Str(o Object, key string) string  { v, _ := o[key].(string); return v }
func Items(o Object, key string) []any { v, _ := o[key].([]any); return v }
func As(v any) Object                  { o, _ := v.(map[string]any); return o }
