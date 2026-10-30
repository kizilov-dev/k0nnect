package wireguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"k0nnect/internal/model"
	linux "k0nnect/internal/network/linux"
	"k0nnect/internal/transport"
)

const protocol = "242"

type Driver struct {
	Config model.Config
	Node   model.Node
	Runner linux.Runner
}
type link struct {
	Name  string   `json:"ifname"`
	Alias string   `json:"ifalias"`
	MTU   int      `json:"mtu"`
	Flags []string `json:"flags"`
	Info  struct {
		Kind string `json:"info_kind"`
	} `json:"linkinfo"`
}
type route struct {
	Destination string          `json:"dst"`
	Device      string          `json:"dev"`
	Protocol    json.RawMessage `json:"protocol"`
	Table       json.RawMessage `json:"table"`
	Type        string          `json:"type"`
	Metric      int             `json:"metric"`
}
type snapshot struct {
	Link           *link
	Routes         []route
	AddressPresent bool
	GuardPresent   bool
}

func (d *Driver) Capabilities() transport.Capabilities {
	return transport.Capabilities{IPv4: true, Encrypted: true}
}
func (d *Driver) owner() string { return "k0nnect:v1:" + d.Config.Cluster + ":" + d.Node.Name }
func (d *Driver) run(ctx context.Context, program string, args ...string) ([]byte, error) {
	return d.Runner.Run(ctx, program, args, nil)
}
func rawString(b json.RawMessage) string {
	if string(b) == "null" {
		return ""
	}
	return strings.Trim(string(b), `"`)
}
func (d *Driver) isGuard(r route) bool {
	return r.Destination == d.Config.Transport.CIDR && r.Type == "blackhole" && rawString(r.Protocol) == protocol && r.Metric == 32760 && mainTable(r)
}
func mainTable(r route) bool { t := rawString(r.Table); return t == "" || t == "main" || t == "254" }
func (d *Driver) isPeer(r route) bool {
	p, err := routePrefix(r.Destination)
	return err == nil && p.Bits() == 32 && r.Device == d.Config.Transport.Interface && rawString(r.Protocol) == protocol && r.Metric == 10 && (r.Type == "" || r.Type == "unicast") && mainTable(r)
}
func routePrefix(s string) (netip.Prefix, error) {
	if a, err := netip.ParseAddr(s); err == nil {
		return netip.PrefixFrom(a, 32), nil
	}
	return netip.ParsePrefix(s)
}
func (d *Driver) inspect(ctx context.Context, endpoints bool) (snapshot, error) {
	var s snapshot
	if err := d.Config.Validate(); err != nil {
		return s, err
	}
	canonical, err := d.Config.Node(d.Node.Name)
	if err != nil || canonical != d.Node {
		return s, fmt.Errorf("driver node does not match inventory")
	}
	b, err := d.run(ctx, "ip", "-j", "-d", "link", "show")
	if err != nil {
		return s, err
	}
	var links []link
	if err = json.Unmarshal(b, &links); err != nil {
		return s, err
	}
	for i := range links {
		if links[i].Name != d.Config.Transport.Interface {
			continue
		}
		if links[i].Alias != d.owner() || links[i].Info.Kind != "wireguard" {
			return s, fmt.Errorf("refusing foreign interface %s", links[i].Name)
		}
		s.Link = &links[i]
	}
	// Policy routing is intentionally outside the first compatibility profile.
	b, err = d.run(ctx, "ip", "-4", "-j", "rule", "show")
	if err != nil {
		return s, err
	}
	var rules []struct {
		Priority int             `json:"priority"`
		Table    json.RawMessage `json:"table"`
		Source   string          `json:"src"`
	}
	if err = json.Unmarshal(b, &rules); err != nil {
		return s, err
	}
	if len(rules) != 3 {
		return s, fmt.Errorf("custom policy routing is unsupported")
	}
	var rawRules []map[string]any
	if err = json.Unmarshal(b, &rawRules); err != nil {
		return s, err
	}
	for _, r := range rawRules {
		for key := range r {
			if key != "priority" && key != "table" && key != "src" && key != "protocol" {
				return s, fmt.Errorf("policy rule selector %s is unsupported", key)
			}
		}
	}
	seenRules := map[int]bool{}
	for _, r := range rules {
		if seenRules[r.Priority] || r.Source != "all" {
			return s, fmt.Errorf("custom policy routing is unsupported")
		}
		seenRules[r.Priority] = true
		t := rawString(r.Table)
		if !((r.Priority == 0 && (t == "local" || t == "255")) || (r.Priority == 32766 && (t == "main" || t == "254")) || (r.Priority == 32767 && (t == "default" || t == "253"))) {
			return s, fmt.Errorf("custom policy routing is unsupported")
		}
	}
	if s.Link != nil {
		b, err = d.run(ctx, "ip", "-4", "-j", "address", "show", "dev", d.Config.Transport.Interface)
		if err != nil {
			return s, err
		}
		var addresses []struct {
			Info []struct {
				Local string `json:"local"`
				Bits  int    `json:"prefixlen"`
			} `json:"addr_info"`
		}
		if err = json.Unmarshal(b, &addresses); err != nil {
			return s, err
		}
		for _, a := range addresses {
			for _, v := range a.Info {
				if v.Local != d.Node.Address || v.Bits != 32 {
					return s, fmt.Errorf("unexpected address on owned interface; address migration requires explicit removal")
				}
				s.AddressPresent = true
			}
		}
	}
	b, err = d.run(ctx, "ip", "-4", "-j", "route", "show", "table", "all")
	if err != nil {
		return s, err
	}
	var routes []route
	if err = json.Unmarshal(b, &routes); err != nil {
		return s, err
	}
	network := netip.MustParsePrefix(d.Config.Transport.CIDR)
	for _, r := range routes {
		if r.Destination == "default" || r.Destination == "0.0.0.0/0" {
			continue
		}
		p, e := routePrefix(r.Destination)
		if e != nil {
			return s, fmt.Errorf("unrecognized route destination %q", r.Destination)
		}
		if !p.Overlaps(network) {
			continue
		}
		// Address-generated local routes are owned by the kernel, not reconciled.
		if s.Link != nil && r.Device == d.Config.Transport.Interface && rawString(r.Protocol) == "kernel" && r.Type == "local" && p.String() == d.Node.Address+"/32" {
			continue
		}
		if s.Link == nil || !(d.isGuard(r) || d.isPeer(r)) {
			return s, fmt.Errorf("transport CIDR conflicts with route %s (dev=%s, table=%s)", r.Destination, r.Device, rawString(r.Table))
		}
		if d.isGuard(r) {
			s.GuardPresent = true
		} else {
			s.Routes = append(s.Routes, r)
		}
	}
	if endpoints {
		for _, n := range d.Config.Nodes {
			if n.Name != d.Node.Name {
				ep := netip.MustParseAddrPort(n.Endpoint)
				b, err = d.run(ctx, "ip", "-4", "-j", "route", "get", ep.Addr().String())
				if err != nil {
					return s, err
				}
				var paths []route
				if err = json.Unmarshal(b, &paths); err != nil {
					return s, err
				}
				if len(paths) != 1 || paths[0].Device == "" || paths[0].Device == d.Config.Transport.Interface || (paths[0].Type != "" && paths[0].Type != "unicast") {
					return s, fmt.Errorf("endpoint %s has no independent underlay route", n.Endpoint)
				}
			}
		}
	}
	return s, nil
}

func ReadPrivateKey(path string) (string, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", fmt.Errorf("open private key: %w", err)
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || stat.Uid != uint32(os.Geteuid()) {
		return "", fmt.Errorf("private key must be a regular owner-only file owned by the executing user")
	}
	b, err := io.ReadAll(io.LimitReader(f, 128))
	if err != nil {
		return "", err
	}
	key := strings.TrimSpace(string(b))
	if !model.KeyValid(key) {
		return "", fmt.Errorf("invalid private key")
	}
	return key, nil
}
func (d *Driver) key(ctx context.Context) (string, error) {
	k, err := ReadPrivateKey(d.Node.WireGuard.PrivateKeyFile)
	if err != nil {
		return "", err
	}
	b, err := d.Runner.Run(ctx, "wg", []string{"pubkey"}, []byte(k+"\n"))
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(b)) != d.Node.WireGuard.PublicKey {
		return "", fmt.Errorf("private key does not match inventory public key")
	}
	return k, nil
}
func (d *Driver) Preflight(ctx context.Context) error {
	if _, err := d.inspect(ctx, true); err != nil {
		return err
	}
	_, err := d.key(ctx)
	return err
}
func (d *Driver) configuration(key string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "[Interface]\nPrivateKey = %s\nListenPort = %d\n", key, netip.MustParseAddrPort(d.Node.Endpoint).Port())
	for _, n := range d.Config.Nodes {
		if n.Name != d.Node.Name {
			fmt.Fprintf(&b, "\n[Peer]\nPublicKey = %s\nEndpoint = %s\nAllowedIPs = %s/32\nPersistentKeepalive = 25\n", n.WireGuard.PublicKey, n.Endpoint, n.Address)
		}
	}
	return []byte(b.String())
}

type undo func(context.Context) error

// Reconcile rolls back successful steps if a subsequent command fails. A kernel
// or host crash still requires rerunning the last known good inventory.
func (d *Driver) Reconcile(ctx context.Context) (result error) {
	s, err := d.inspect(ctx, true)
	if err != nil {
		return err
	}
	k, err := d.key(ctx)
	if err != nil {
		return err
	}
	var undos []undo
	defer func() {
		if result == nil {
			return
		}
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for i := len(undos) - 1; i >= 0; i-- {
			if e := undos[i](rollbackCtx); e != nil {
				result = errors.Join(result, fmt.Errorf("rollback: %w", e))
			}
		}
	}()
	step := func(args, reverse []string) error {
		if _, e := d.run(ctx, "ip", args...); e != nil {
			return e
		}
		if reverse != nil {
			undos = append(undos, func(c context.Context) error { _, e := d.run(c, "ip", reverse...); return e })
		}
		return nil
	}
	iface := d.Config.Transport.Interface
	if s.Link == nil {
		if err = step([]string{"link", "add", "dev", iface, "type", "wireguard"}, []string{"link", "delete", "dev", iface}); err != nil {
			return err
		}
		if err = step([]string{"link", "set", "dev", iface, "alias", d.owner()}, nil); err != nil {
			return err
		}
	}
	oldMTU := 1420
	if s.Link != nil {
		oldMTU = s.Link.MTU
	}
	if err = step([]string{"link", "set", "dev", iface, "mtu", strconv.Itoa(d.Config.Transport.MTU)}, []string{"link", "set", "dev", iface, "mtu", strconv.Itoa(oldMTU)}); err != nil {
		return err
	}
	if !s.GuardPresent {
		args := []string{"-4", "route", "add", "blackhole", d.Config.Transport.CIDR, "proto", protocol, "metric", "32760"}
		reverse := append([]string(nil), args...)
		reverse[2] = "del"
		if err = step(args, reverse); err != nil {
			return err
		}
	}
	var previous []byte
	if s.Link != nil {
		previous, err = d.run(ctx, "wg", "showconf", iface)
		if err != nil {
			return err
		}
	}
	if s.Link != nil {
		undos = append(undos, func(c context.Context) error {
			_, e := d.Runner.Run(c, "wg", []string{"syncconf", iface, "/dev/stdin"}, previous)
			return e
		})
	}
	if _, err = d.Runner.Run(ctx, "wg", []string{"syncconf", iface, "/dev/stdin"}, d.configuration(k)); err != nil {
		return err
	}
	if !s.AddressPresent {
		if err = step([]string{"-4", "address", "add", d.Node.Address + "/32", "dev", iface}, []string{"-4", "address", "del", d.Node.Address + "/32", "dev", iface}); err != nil {
			return err
		}
	}
	// Linux requires the nexthop device to be UP before adding peer routes.
	wasUp := false
	if s.Link != nil {
		for _, f := range s.Link.Flags {
			if f == "UP" {
				wasUp = true
			}
		}
	}
	if !wasUp {
		if err = step([]string{"link", "set", "dev", iface, "up"}, []string{"link", "set", "dev", iface, "down"}); err != nil {
			return err
		}
	}
	present := map[string]bool{}
	for _, r := range s.Routes {
		p, _ := routePrefix(r.Destination)
		present[p.String()] = true
	}
	wanted := map[string]bool{}
	peerArgs := func(action, prefix string) []string {
		return []string{"-4", "route", action, prefix, "dev", iface, "proto", protocol, "metric", "10"}
	}
	for _, n := range d.Config.Nodes {
		if n.Name != d.Node.Name {
			prefix := n.Address + "/32"
			wanted[prefix] = true
			if !present[prefix] {
				if err = step(peerArgs("add", prefix), peerArgs("del", prefix)); err != nil {
					return err
				}
			}
		}
	}
	for prefix := range present {
		if !wanted[prefix] {
			if err = step(peerArgs("del", prefix), peerArgs("add", prefix)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *Driver) Status(ctx context.Context) ([]transport.Status, error) {
	s, err := d.inspect(ctx, false)
	if err != nil {
		return nil, err
	}
	if s.Link == nil {
		return nil, fmt.Errorf("transport is not installed")
	}
	statuses := make(map[string]*transport.Status)
	var order []string
	for _, n := range d.Config.Nodes {
		if n.Name != d.Node.Name {
			statuses[n.WireGuard.PublicKey] = &transport.Status{Node: n.Name}
			order = append(order, n.WireGuard.PublicKey)
		}
	}
	// Never use `wg show ... dump`: that output includes the private key.
	for _, field := range []string{"latest-handshakes", "transfer"} {
		b, e := d.run(ctx, "wg", "show", d.Config.Transport.Interface, field)
		if e != nil {
			return nil, e
		}
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			v := strings.Fields(line)
			if len(v) < 2 {
				continue
			}
			st := statuses[v[0]]
			if st == nil {
				continue
			}
			if field == "latest-handshakes" {
				st.LastHandshake, e = strconv.ParseInt(v[1], 10, 64)
			} else if len(v) == 3 {
				st.Received, e = strconv.ParseUint(v[1], 10, 64)
				if e == nil {
					st.Sent, e = strconv.ParseUint(v[2], 10, 64)
				}
			} else {
				return nil, fmt.Errorf("invalid wg transfer output")
			}
			if e != nil {
				return nil, fmt.Errorf("invalid wg status output")
			}
		}
	}
	out := make([]transport.Status, 0, len(order))
	for _, key := range order {
		out = append(out, *statuses[key])
	}
	return out, nil
}
func (d *Driver) Remove(ctx context.Context) error {
	s, err := d.inspect(ctx, false)
	if err != nil {
		return err
	}
	if s.Link == nil {
		return nil
	}
	if _, err = d.run(ctx, "ip", "link", "delete", "dev", d.Config.Transport.Interface); err != nil {
		return err
	}
	if s.GuardPresent {
		_, err = d.run(ctx, "ip", "-4", "route", "del", "blackhole", d.Config.Transport.CIDR, "proto", protocol, "metric", "32760")
	}
	return err
}
