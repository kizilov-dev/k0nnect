package model

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
)

// Config describes a single cluster-extension network. It contains no private keys.
type Config struct {
	Version    int        `json:"version"`
	Mode       string     `json:"mode"`
	Cluster    string     `json:"cluster"`
	Transport  Transport  `json:"transport"`
	Kubernetes Kubernetes `json:"kubernetes"`
	Nodes      []Node     `json:"nodes"`
}
type Transport struct {
	Driver    string `json:"driver"`
	Interface string `json:"interface"`
	CIDR      string `json:"cidr"`
	MTU       int    `json:"mtu"`
	PathMTU   int    `json:"pathMTU"`
}
type Kubernetes struct {
	Distribution string `json:"distribution"`
	APIServer    string `json:"apiServer"`
	PodCIDR      string `json:"podCIDR"`
	ServiceCIDR  string `json:"serviceCIDR"`
	CNI          CNI    `json:"cni"`
}
type CNI struct {
	Name          string `json:"name"`
	Backend       string `json:"backend"`
	DirectRouting bool   `json:"directRouting"`
}
type Node struct {
	Name      string        `json:"name"`
	Address   string        `json:"address"`
	Endpoint  string        `json:"endpoint"`
	WireGuard WireGuardNode `json:"wireguard"`
}
type WireGuardNode struct {
	PublicKey      string `json:"publicKey"`
	PrivateKeyFile string `json:"privateKeyFile,omitempty"`
}

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
var interfacePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,14}$`)

func Load(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer f.Close()
	return Decode(f)
}
func Decode(r io.Reader) (Config, error) {
	var c Config
	d := json.NewDecoder(io.LimitReader(r, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, fmt.Errorf("inventory: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return c, fmt.Errorf("inventory must contain exactly one JSON object")
	}
	return c, c.Validate()
}
func KeyValid(s string) bool {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != 32 || base64.StdEncoding.EncodeToString(b) != s {
		return false
	}
	for _, v := range b {
		if v != 0 {
			return true
		}
	}
	return false
}
func (c Config) Validate() error {
	if c.Version != 1 || c.Mode != "cluster-extension" {
		return fmt.Errorf("only version=1, mode=cluster-extension is supported; multi-cluster is a separate future mode")
	}
	if !namePattern.MatchString(c.Cluster) {
		return fmt.Errorf("invalid cluster name")
	}
	if c.Transport.Driver != "wireguard" {
		return fmt.Errorf("unsupported transport %q", c.Transport.Driver)
	}
	if !interfacePattern.MatchString(c.Transport.Interface) {
		return fmt.Errorf("invalid interface name")
	}
	if c.Transport.PathMTU < 1280 || c.Transport.PathMTU > 9000 || c.Transport.MTU < 1280 || c.Transport.MTU > c.Transport.PathMTU-60 {
		return fmt.Errorf("MTU must be >=1280 and <= pathMTU-60 (IPv4 WireGuard overhead); pathMTU must be 1280..9000")
	}
	if c.Kubernetes.Distribution != "kubeadm" || c.Kubernetes.CNI.Name != "flannel" || c.Kubernetes.CNI.Backend != "vxlan" || c.Kubernetes.CNI.DirectRouting {
		return fmt.Errorf("unsupported profile: requires kubeadm + flannel/vxlan, directRouting=false")
	}
	prefixes := make([]netip.Prefix, 0, 3)
	for _, s := range []string{c.Transport.CIDR, c.Kubernetes.PodCIDR, c.Kubernetes.ServiceCIDR} {
		p, err := netip.ParsePrefix(s)
		if err != nil || !p.Addr().Is4() || p != p.Masked() || p.Bits() < 8 || p.Bits() > 30 || !p.Addr().IsPrivate() {
			return fmt.Errorf("CIDR %q must be a canonical private IPv4 prefix /8../30", s)
		}
		for _, q := range prefixes {
			if p.Overlaps(q) {
				return fmt.Errorf("overlapping CIDRs: %s and %s", p, q)
			}
		}
		prefixes = append(prefixes, p)
	}
	if len(c.Nodes) < 2 {
		return fmt.Errorf("at least two nodes required")
	}
	names, addresses, keys, endpoints := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, n := range c.Nodes {
		if !namePattern.MatchString(n.Name) || names[n.Name] {
			return fmt.Errorf("invalid or duplicate node name %q", n.Name)
		}
		names[n.Name] = true
		a, err := netip.ParseAddr(n.Address)
		if err != nil || !a.Is4() || !prefixes[0].Contains(a) || a == prefixes[0].Addr() || a == lastAddr(prefixes[0]) || addresses[n.Address] {
			return fmt.Errorf("invalid or duplicate transport address on %s", n.Name)
		}
		addresses[n.Address] = true
		if !KeyValid(n.WireGuard.PublicKey) || keys[n.WireGuard.PublicKey] {
			return fmt.Errorf("invalid or duplicate public key on %s", n.Name)
		}
		keys[n.WireGuard.PublicKey] = true
		ep, err := netip.ParseAddrPort(n.Endpoint)
		if err != nil || !ep.Addr().Is4() || !ep.Addr().IsGlobalUnicast() || ep.Port() == 0 || endpoints[n.Endpoint] {
			return fmt.Errorf("endpoint on %s must be a unique IPv4:port", n.Name)
		}
		endpoints[n.Endpoint] = true
		for _, p := range prefixes {
			if p.Contains(ep.Addr()) {
				return fmt.Errorf("endpoint on %s overlaps internal CIDR %s", n.Name, p)
			}
		}
		if n.WireGuard.PrivateKeyFile != "" && !filepath.IsAbs(n.WireGuard.PrivateKeyFile) {
			return fmt.Errorf("privateKeyFile on %s must be absolute", n.Name)
		}
	}
	ep, err := netip.ParseAddrPort(c.Kubernetes.APIServer)
	if err != nil || ep.Port() == 0 || !addresses[ep.Addr().String()] {
		return fmt.Errorf("apiServer must be an inventory transport address with port (no DNS or load balancer in MVP)")
	}
	return nil
}
func lastAddr(p netip.Prefix) netip.Addr {
	a := p.Addr().As4()
	for bit := p.Bits(); bit < 32; bit++ {
		a[bit/8] |= 1 << (7 - bit%8)
	}
	return netip.AddrFrom4(a)
}
func (c Config) Node(name string) (Node, error) {
	for _, n := range c.Nodes {
		if n.Name == name {
			return n, nil
		}
	}
	return Node{}, fmt.Errorf("node %q not in inventory", name)
}
