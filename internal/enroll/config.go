package enroll

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"regexp"
)

type Config struct {
	Name           string `json:"name"`
	NodeName       string `json:"nodeName"`
	Namespace      string `json:"namespace"`
	Host           string `json:"host"`
	SSHPort        int    `json:"sshPort"`
	User           string `json:"user"`
	PasswordFile   string `json:"passwordFile,omitempty"`
	PrivateKeyFile string `json:"privateKeyFile,omitempty"`
	HostKeySHA256  string `json:"hostKeySHA256"`
	TransportCIDR  string `json:"transportCIDR"`
	WorkerAddress  string `json:"workerAddress"`
	Port           int    `json:"port"`
	MTU            int    `json:"mtu"`
	K0sVersion     string `json:"k0sVersion,omitempty"`
	ClusterID      string `json:"-"`
	CNI            string `json:"-"`
	// GatewayHostPortOnly serves the gateway WireGuard port without a NodePort Service.
	GatewayHostPortOnly bool `json:"-"`
}

var safeName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,40}$`)

func Load(path string) (Config, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return Config{}, e
	}
	var c Config
	e = json.Unmarshal(b, &c)
	if e != nil {
		return c, e
	}
	if e = c.Validate(); e != nil {
		return c, e
	}
	return c, nil
}
func (c *Config) Validate() error {
	if c.Namespace == "" {
		c.Namespace = "k8nnect-system"
	}
	if c.User == "" {
		c.User = "root"
	}
	if c.SSHPort == 0 {
		c.SSHPort = 22
	}
	if c.Port == 0 {
		c.Port = 31820
	}
	if c.MTU == 0 {
		c.MTU = 1400
	}
	if !safeName.MatchString(c.Name) || !safeName.MatchString(c.NodeName) || !safeName.MatchString(c.Namespace) {
		return fmt.Errorf("invalid connection, node or namespace name")
	}
	if c.User != "root" {
		return fmt.Errorf("initial installer requires root SSH account")
	}
	host, e := netip.ParseAddr(c.Host)
	if e != nil || !host.Is4() || !host.IsGlobalUnicast() {
		return fmt.Errorf("literal VDS IPv4 address required")
	}
	p, e := netip.ParsePrefix(c.TransportCIDR)
	if e != nil || !p.Addr().Is4() || p != p.Masked() || !p.Addr().IsPrivate() || p.Bits() != 24 {
		return fmt.Errorf("transportCIDR must be a private canonical IPv4 /24")
	}
	a, e := netip.ParseAddr(c.WorkerAddress)
	if e != nil || !p.Contains(a) || a == p.Addr() || a.As4()[3] < 100 || a.As4()[3] == 255 {
		return fmt.Errorf("workerAddress must be in transport /24, with last octet 100..254")
	}
	if c.Port < 30000 || c.Port > 32766 || c.MTU < 1280 || c.MTU > 1440 || c.SSHPort < 1 || c.SSHPort > 65535 {
		return fmt.Errorf("invalid port or MTU")
	}
	if len(c.HostKeySHA256) < 20 {
		return fmt.Errorf("pinned SSH hostKeySHA256 required")
	}
	return nil
}

type Peer struct {
	Name       string `json:"name"`
	Address    string `json:"address"`
	InternalIP string `json:"internalIP"`
	Endpoint   string `json:"endpoint"`
	PublicKey  string `json:"publicKey"`
}
type Network struct {
	Name           string `json:"name"`
	NodeName       string `json:"nodeName"`
	CIDR           string `json:"cidr"`
	Address        string `json:"address"`
	WorkerAddress  string `json:"workerAddress"`
	WorkerEndpoint string `json:"workerEndpoint"`
	WorkerKey      string `json:"workerKey"`
	Interface      string `json:"interface"`
	Port           int    `json:"port"`
	MTU            int    `json:"mtu"`
	// NodeMTU is the MTU of the existing nodes' interfaces. The Worker eth1
	// carries it so the CNI on the VDS derives the same Pod MTU as elsewhere.
	NodeMTU int    `json:"nodeMTU,omitempty"`
	Peers   []Peer `json:"peers"`
}
