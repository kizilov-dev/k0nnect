package model

import (
	"os"
	"strings"
	"testing"
)

func example(t *testing.T) Config {
	t.Helper()
	c, e := Load("../../examples/inventory.json")
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func TestValidationRejectsUnsupportedAndConflictingConfigurations(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Config)
	}{
		{"multi-cluster", func(c *Config) { c.Mode = "cluster-interconnect" }},
		{"CNI", func(c *Config) { c.Kubernetes.CNI.Name = "cilium" }},
		{"host-gw", func(c *Config) { c.Kubernetes.CNI.Backend = "host-gw" }},
		{"direct routing", func(c *Config) { c.Kubernetes.CNI.DirectRouting = true }},
		{"transport", func(c *Config) { c.Transport.Driver = "ipsec" }},
		{"pod overlap", func(c *Config) { c.Kubernetes.PodCIDR = c.Transport.CIDR }},
		{"service overlap", func(c *Config) { c.Kubernetes.ServiceCIDR = c.Kubernetes.PodCIDR }},
		{"MTU", func(c *Config) { c.Transport.MTU = 1450 }},
		{"node address", func(c *Config) { c.Nodes[1].Address = c.Nodes[0].Address }},
		{"network address", func(c *Config) { c.Nodes[1].Address = "10.203.0.0" }},
		{"broadcast address", func(c *Config) { c.Nodes[1].Address = "10.203.0.255" }},
		{"key", func(c *Config) { c.Nodes[1].WireGuard.PublicKey = c.Nodes[0].WireGuard.PublicKey }},
		{"endpoint recursion", func(c *Config) { c.Nodes[1].Endpoint = "10.203.0.2:51820" }},
		{"DNS endpoint", func(c *Config) { c.Nodes[1].Endpoint = "example.com:51820" }},
		{"IPv6", func(c *Config) { c.Nodes[1].Address = "fd00::1" }},
		{"interface injection", func(c *Config) { c.Transport.Interface = "a;ip link" }},
		{"relative secret", func(c *Config) { c.Nodes[0].WireGuard.PrivateKeyFile = "node.key" }},
		{"unknown API", func(c *Config) { c.Kubernetes.APIServer = "10.203.0.50:6443" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := example(t)
			tt.change(&c)
			if c.Validate() == nil {
				t.Fatal("accepted invalid configuration")
			}
		})
	}
}
func TestDecodeRejectsUnknownAndTrailingData(t *testing.T) {
	b, e := os.ReadFile("../../examples/inventory.json")
	if e != nil {
		t.Fatal(e)
	}
	for _, s := range []string{strings.Replace(string(b), `"version": 1`, `"version": 1, "typo": true`, 1), string(b) + " {}"} {
		if _, e = Decode(strings.NewReader(s)); e == nil {
			t.Fatal("accepted ambiguous inventory")
		}
	}
}
