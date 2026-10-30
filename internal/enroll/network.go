package enroll

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"k0nnect/internal/kube"
	"k0nnect/internal/model"
	linux "k0nnect/internal/network/linux"
)

const WorkerNS = "k0nnect"
const GatewayInterface = "k0ngw0"

func command(ctx context.Context, p string, args ...string) ([]byte, error) {
	return (linux.ExecRunner{}).Run(ctx, p, args, nil)
}
func privateKey(ctx context.Context, path string) (string, error) {
	if b, e := os.ReadFile(path); e == nil {
		key := strings.TrimSpace(string(b))
		if !model.KeyValid(key) {
			return "", fmt.Errorf("invalid managed WireGuard key")
		}
		return key, nil
	}
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return "", e
	}
	b, e := command(ctx, "wg", "genkey")
	if e != nil {
		return "", e
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return "", e
	}
	_, e = f.Write(b)
	e2 := f.Close()
	if e != nil {
		return "", e
	}
	if e2 != nil {
		return "", e2
	}
	return strings.TrimSpace(string(b)), nil
}
func publicKey(ctx context.Context, key string) (string, error) {
	b, e := (linux.ExecRunner{}).Run(ctx, "wg", []string{"pubkey"}, []byte(key+"\n"))
	return strings.TrimSpace(string(b)), e
}
func validateNetwork(n Network) error {
	if !safeName.MatchString(n.Name) {
		return fmt.Errorf("invalid network owner")
	}
	p, e := netip.ParsePrefix(n.CIDR)
	if e != nil || !p.Addr().Is4() || !p.Addr().IsPrivate() || p.Bits() != 24 || p != p.Masked() {
		return fmt.Errorf("invalid network CIDR")
	}
	for _, s := range []string{n.Address, n.WorkerAddress} {
		a, e := netip.ParseAddr(s)
		if e != nil || !a.Is4() || !p.Contains(a) {
			return fmt.Errorf("invalid network address")
		}
	}
	if n.MTU < 1280 || n.MTU > 1440 || n.Port < 30000 || n.Port > 32767 {
		return fmt.Errorf("invalid network MTU or port")
	}
	if n.NodeMTU != 0 && (n.NodeMTU < n.MTU || n.NodeMTU > 9000) {
		return fmt.Errorf("invalid node MTU")
	}
	if !model.KeyValid(n.WorkerKey) {
		return fmt.Errorf("invalid Worker public key")
	}
	if _, e = netip.ParseAddrPort(n.WorkerEndpoint); e != nil {
		return e
	}
	for _, peer := range n.Peers {
		if !model.KeyValid(peer.PublicKey) {
			return fmt.Errorf("invalid gateway public key")
		}
		if _, e = netip.ParseAddrPort(peer.Endpoint); peer.Endpoint != "" && e != nil {
			return e
		}
		for _, a := range []string{peer.Address, peer.InternalIP} {
			if ip, e := netip.ParseAddr(a); e != nil || !ip.Is4() {
				return fmt.Errorf("invalid gateway address")
			}
		}
	}
	return nil
}
func loadNetwork(path string) (Network, error) {
	var n Network
	b, e := os.ReadFile(path)
	if e != nil {
		return n, e
	}
	if e = json.Unmarshal(b, &n); e != nil {
		return n, e
	}
	return n, validateNetwork(n)
}
func linkExists(ctx context.Context, ns, iface, owner string) (bool, error) {
	args := []string{"-j", "-d", "link", "show"}
	if ns != "" {
		args = append([]string{"-n", ns}, args...)
	}
	b, e := command(ctx, "ip", args...)
	if e != nil {
		return false, e
	}
	var links []struct {
		Name  string `json:"ifname"`
		Alias string `json:"ifalias"`
		Info  struct {
			Kind string `json:"info_kind"`
		} `json:"linkinfo"`
	}
	if e = json.Unmarshal(b, &links); e != nil {
		return false, e
	}
	for _, l := range links {
		if l.Name == iface {
			if l.Alias != owner || l.Info.Kind != "wireguard" {
				return false, fmt.Errorf("refusing foreign interface %s", iface)
			}
			return true, nil
		}
	}
	return false, nil
}
func syncWG(ctx context.Context, ns, iface string, config []byte) error {
	p := "wg"
	args := []string{"syncconf", iface, "/dev/stdin"}
	if ns != "" {
		p = "ip"
		args = append([]string{"netns", "exec", ns, "wg"}, args...)
	}
	// ip may pass wg errors through; suppress all output because input contains a key.
	cmd := exec.CommandContext(ctx, p, args...)
	cmd.Stdin = strings.NewReader(string(config))
	if e := cmd.Run(); e != nil {
		return fmt.Errorf("WireGuard configuration failed: %w", e)
	}
	return nil
}
func ensureGateway(ctx context.Context, n Network, key string) error {
	owner := "k0nnect:gateway:" + n.Name
	exists, e := linkExists(ctx, "", GatewayInterface, owner)
	if e != nil {
		return e
	}
	if !exists {
		// Detect an overlap before taking ownership of any host routes.
		b, e := command(ctx, "ip", "-4", "-j", "route", "show", "table", "all")
		if e != nil {
			return e
		}
		var routes []struct {
			Dst string `json:"dst"`
		}
		if e = json.Unmarshal(b, &routes); e != nil {
			return e
		}
		network := netip.MustParsePrefix(n.CIDR)
		for _, r := range routes {
			if r.Dst == "default" {
				continue
			}
			p, e := netip.ParsePrefix(r.Dst)
			if e != nil {
				a, err := netip.ParseAddr(r.Dst)
				if err != nil {
					continue
				}
				p = netip.PrefixFrom(a, 32)
			}
			if p.Overlaps(network) {
				return fmt.Errorf("gateway transport conflicts with existing route %s", r.Dst)
			}
		}
		if _, e = command(ctx, "ip", "link", "add", GatewayInterface, "type", "wireguard"); e != nil {
			return e
		}
		if _, e = command(ctx, "ip", "link", "set", GatewayInterface, "alias", owner); e != nil {
			return e
		}
	}
	cfg := fmt.Sprintf("[Interface]\nPrivateKey = %s\nListenPort = %d\n\n[Peer]\nPublicKey = %s\nEndpoint = %s\nAllowedIPs = %s/32\nPersistentKeepalive = 25\n", key, n.Port, n.WorkerKey, n.WorkerEndpoint, n.WorkerAddress)
	if e = syncWG(ctx, "", GatewayInterface, []byte(cfg)); e != nil {
		return e
	}
	for _, args := range [][]string{{"address", "replace", n.Address + "/32", "dev", GatewayInterface}, {"link", "set", GatewayInterface, "mtu", strconv.Itoa(n.MTU), "up"}, {"route", "replace", n.WorkerAddress + "/32", "dev", GatewayInterface, "proto", "242", "metric", "10"}} {
		if _, e = command(ctx, "ip", args...); e != nil {
			return e
		}
	}
	forwarding, e := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	if e != nil {
		return e
	}
	if strings.TrimSpace(string(forwarding)) != "1" {
		return fmt.Errorf("gateway requires existing IPv4 forwarding enabled by the CNI")
	}
	ownerComment := "k0nnect:" + n.Name
	rules := [][]string{
		{"-t", "nat", "POSTROUTING", "-s", n.WorkerAddress + "/32", "!", "-o", GatewayInterface, "-m", "comment", "--comment", ownerComment, "-j", "MASQUERADE"},
		{"-t", "filter", "FORWARD", "-i", GatewayInterface, "-s", n.WorkerAddress + "/32", "-m", "comment", "--comment", ownerComment, "-j", "ACCEPT"},
		{"-t", "filter", "FORWARD", "-o", GatewayInterface, "-d", n.WorkerAddress + "/32", "-m", "comment", "--comment", ownerComment, "-j", "ACCEPT"},
	}
	for _, r := range rules {
		check := append([]string{"-w", "5", r[0], r[1], "-C"}, r[2:]...)
		if _, e = command(ctx, "iptables", check...); e != nil {
			add := append([]string{"-w", "5", r[0], r[1], "-I", r[2], "1"}, r[3:]...)
			if _, e = command(ctx, "iptables", add...); e != nil {
				return e
			}
		}
	}
	return nil
}
func EnsureWorker(ctx context.Context, path string) error {
	n, e := loadNetwork(path)
	if e != nil {
		return e
	}
	if len(n.Peers) == 0 {
		return fmt.Errorf("no gateways")
	}
	key, e := privateKey(ctx, "/etc/k0nnect/worker.key")
	if e != nil {
		return e
	}
	pub, e := publicKey(ctx, key)
	if e != nil {
		return e
	}
	if pub != n.WorkerKey {
		return fmt.Errorf("Worker private key does not match network configuration")
	}
	if _, e = os.Stat("/run/netns/" + WorkerNS); os.IsNotExist(e) {
		if _, e = command(ctx, "ip", "netns", "add", WorkerNS); e != nil {
			return e
		}
	}
	owner := "k0nnect:worker:" + n.Name
	exists, e := linkExists(ctx, WorkerNS, "eth1", owner)
	if e != nil {
		return e
	}
	if !exists {
		// The UDP socket must be born in the host namespace. WireGuard retains that
		// socket after moving the interface; outer packets use the physical network.
		tmp := "k0nworker0"
		existing, e := linkExists(ctx, "", tmp, owner)
		if e != nil {
			return e
		}
		if !existing {
			if _, e = command(ctx, "ip", "link", "add", tmp, "type", "wireguard"); e != nil {
				return e
			}
			if _, e = command(ctx, "ip", "link", "set", tmp, "alias", owner); e != nil {
				return e
			}
		}
		if _, e = command(ctx, "ip", "link", "set", tmp, "netns", WorkerNS); e != nil {
			return e
		}
		if _, e = command(ctx, "ip", "-n", WorkerNS, "link", "set", tmp, "name", "eth1"); e != nil {
			return e
		}
	}
	if e = syncWG(ctx, WorkerNS, "eth1", workerConfiguration(n, key)); e != nil {
		return e
	}
	// CNIs derive Pod MTU from eth1. A smaller value than on existing nodes makes
	// the VDS Pods silently drop larger packets arriving through the CNI tunnel,
	// so eth1 matches the nodes and WireGuard fragments outer packets instead.
	// The default route keeps the transport MTU for the Worker's own traffic.
	linkMTU := n.MTU
	if n.NodeMTU > linkMTU {
		linkMTU = n.NodeMTU
	}
	for _, args := range [][]string{{"link", "set", "lo", "up"}, {"address", "replace", n.WorkerAddress + "/32", "dev", "eth1"}, {"link", "set", "eth1", "mtu", strconv.Itoa(linkMTU), "up"}, {"route", "replace", "default", "dev", "eth1", "proto", "242", "metric", "10", "mtu", strconv.Itoa(n.MTU)}} {
		if _, e = command(ctx, "ip", append([]string{"-n", WorkerNS}, args...)...); e != nil {
			return e
		}
	}
	_, e = command(ctx, "ip", "netns", "exec", WorkerNS, "sysctl", "-w", "net.ipv4.ip_forward=1")
	return e
}

func workerConfiguration(n Network, key string) []byte {
	var cfg strings.Builder
	fmt.Fprintf(&cfg, "[Interface]\nPrivateKey = %s\nListenPort = %d\n", key, n.Port)
	for i, p := range n.Peers {
		allowed := p.Address + "/32," + p.InternalIP + "/32"
		if i == 0 {
			allowed = "0.0.0.0/0"
		}
		fmt.Fprintf(&cfg, "\n[Peer]\nPublicKey = %s\n", p.PublicKey)
		// Private gateways initiate outbound to the public VDS. Their authenticated
		// endpoint is learned by WireGuard roaming and kept alive across NAT.
		if p.Endpoint != "" {
			fmt.Fprintf(&cfg, "Endpoint = %s\n", p.Endpoint)
		}
		fmt.Fprintf(&cfg, "AllowedIPs = %s\nPersistentKeepalive = 25\n", allowed)
	}
	return []byte(cfg.String())
}

// NodeMTUAnnotation publishes the MTU of the interface carrying a gateway's InternalIP.
const NodeMTUAnnotation = "k0nnect.io/node-mtu"

func addressMTU(ctx context.Context, ip string) (int, error) {
	b, e := command(ctx, "ip", "-j", "-4", "address", "show")
	if e != nil {
		return 0, e
	}
	var links []struct {
		MTU   int `json:"mtu"`
		Addrs []struct {
			Local string `json:"local"`
		} `json:"addr_info"`
	}
	if e = json.Unmarshal(b, &links); e != nil {
		return 0, e
	}
	for _, l := range links {
		for _, a := range l.Addrs {
			if a.Local == ip && l.MTU >= 1280 {
				return l.MTU, nil
			}
		}
	}
	return 0, fmt.Errorf("no interface carries node InternalIP %q", ip)
}

func Agent(ctx context.Context, kind, path string) error {
	if kind == "worker-network" {
		return EnsureWorker(ctx, path)
	}
	if kind == "worker-agent" {
		for {
			if e := EnsureWorker(ctx, path); e != nil {
				fmt.Fprintln(os.Stderr, "worker network reconcile:", e)
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(15 * time.Second):
			}
		}
	}
	c, e := kube.New(ctx, "")
	if e != nil {
		return e
	}
	node := os.Getenv("NODE_NAME")
	namespace := os.Getenv("POD_NAMESPACE")
	if node == "" || namespace == "" {
		return fmt.Errorf("NODE_NAME and POD_NAMESPACE required")
	}
	key, e := privateKey(ctx, "/state/gateway.key")
	if e != nil {
		return e
	}
	pub, e := publicKey(ctx, key)
	if e != nil {
		return e
	}
	self, e := c.Get(ctx, "/api/v1/nodes/"+node)
	if e != nil {
		return e
	}
	mtu, e := addressMTU(ctx, address(self, "InternalIP"))
	if e != nil {
		return e
	}
	if _, e = c.Do(ctx, "PATCH", "/api/v1/nodes/"+node, kube.Object{"metadata": kube.Object{"annotations": kube.Object{"k0nnect.io/wireguard-public-key": pub, NodeMTUAnnotation: strconv.Itoa(mtu)}}}); e != nil {
		return e
	}
	for {
		cm, e := c.Get(ctx, "/api/v1/namespaces/"+namespace+"/configmaps/gateway-network")
		if e == nil {
			var n Network
			e = json.Unmarshal([]byte(kube.Str(kube.Map(cm, "data"), node)), &n)
			if e == nil {
				e = validateNetwork(n)
			}
			if e == nil {
				e = ensureGateway(ctx, n, key)
			}
		}
		if e != nil {
			os.Remove("/tmp/k0nnect-ready")
			fmt.Fprintln(os.Stderr, "gateway network reconcile:", e)
		} else {
			os.WriteFile("/tmp/k0nnect-ready", []byte("ready\n"), 0600)
			fmt.Println("Gateway network configured")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(10 * time.Second):
		}
	}
}
