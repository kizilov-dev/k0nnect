// Package kubeadm renders a worker-only join document. Kubernetes credentials
// are kept separate from the transport inventory.
package kubeadm

import (
	"encoding/json"
	"fmt"
	"k8nnect/internal/model"
	"regexp"
)

var tokenPattern = regexp.MustCompile(`^[a-z0-9]{6}\.[a-z0-9]{16}$`)
var hashPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func Render(c model.Config, node, token, hash string) ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	n, err := c.Node(node)
	if err != nil {
		return nil, err
	}
	if !tokenPattern.MatchString(token) || !hashPattern.MatchString(hash) {
		return nil, fmt.Errorf("invalid bootstrap token or pinned CA hash")
	}
	v := map[string]any{
		"apiVersion": "kubeadm.k8s.io/v1beta4", "kind": "JoinConfiguration",
		"discovery":        map[string]any{"bootstrapToken": map[string]any{"apiServerEndpoint": c.Kubernetes.APIServer, "token": token, "caCertHashes": []string{hash}}},
		"nodeRegistration": map[string]any{"name": n.Name, "kubeletExtraArgs": []map[string]string{{"name": "node-ip", "value": n.Address}}},
	}
	return json.MarshalIndent(v, "", "  ")
}
