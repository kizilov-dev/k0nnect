package wireguard

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"k0nnect/internal/model"
	"k0nnect/internal/transport"
)

var _ transport.Driver = (*Driver)(nil)

// fakeKernel models state rather than supplying a fixed expected command list.
// This exercises reconciliation, repeat application and compensating actions.
type fakeKernel struct {
	links      []link
	routes     []route
	address    bool
	config     []byte
	public     string
	fail       string
	mutations  int
	customRule bool
}

func jsonBytes(v any) []byte { b, _ := json.Marshal(v); return b }
func (f *fakeKernel) Run(_ context.Context, program string, args []string, input []byte) ([]byte, error) {
	s := strings.Join(args, " ")
	if program == "wg" {
		switch args[0] {
		case "pubkey":
			return []byte(f.public + "\n"), nil
		case "showconf":
			return append([]byte(nil), f.config...), nil
		case "syncconf":
			f.mutations++
			if f.fail == s {
				f.fail = ""
				return nil, errors.New("injected failure")
			}
			f.config = append([]byte(nil), input...)
			return nil, nil
		case "show":
			return nil, nil
		}
	}
	if program != "ip" {
		return nil, errors.New("unexpected program")
	}
	switch {
	case s == "-j -d link show":
		return jsonBytes(f.links), nil
	case s == "-4 -j rule show":
		r := []map[string]any{{"priority": 0, "src": "all", "table": "local"}, {"priority": 32766, "src": "all", "table": "main"}, {"priority": 32767, "src": "all", "table": "default"}}
		if f.customRule {
			r[1]["fwmark"] = 1
		}
		return jsonBytes(r), nil
	case strings.HasPrefix(s, "-4 -j address show"):
		info := []map[string]any{}
		if f.address {
			info = append(info, map[string]any{"local": "10.203.0.1", "prefixlen": 32})
		}
		return jsonBytes([]map[string]any{{"addr_info": info}}), nil
	case s == "-4 -j route show table all":
		return jsonBytes(f.routes), nil
	case strings.HasPrefix(s, "-4 -j route get"):
		return []byte(`[{"dev":"eth0"}]`), nil
	}
	f.mutations++
	if f.fail == s {
		f.fail = ""
		return nil, errors.New("injected failure")
	}
	switch {
	case s == "link add dev k0n0 type wireguard":
		l := link{Name: "k0n0", MTU: 1420}
		l.Info.Kind = "wireguard"
		f.links = []link{l}
	case s == "link delete dev k0n0":
		f.links = nil
		f.address = false
		f.config = nil
		var keep []route
		for _, r := range f.routes {
			if r.Device != "k0n0" {
				keep = append(keep, r)
			}
		}
		f.routes = keep
	case strings.HasPrefix(s, "link set dev k0n0 alias "):
		f.links[0].Alias = args[5]
	case strings.HasPrefix(s, "link set dev k0n0 mtu "):
		if args[5] == "1400" {
			f.links[0].MTU = 1400
		} else {
			f.links[0].MTU = 1420
		}
	case s == "link set dev k0n0 up":
		f.links[0].Flags = []string{"UP"}
	case s == "link set dev k0n0 down":
		f.links[0].Flags = nil
	case strings.HasPrefix(s, "-4 address add"):
		f.address = true
	case strings.HasPrefix(s, "-4 address del"):
		f.address = false
	case strings.HasPrefix(s, "-4 route "):
		guard := args[3] == "blackhole"
		if args[2] == "add" && !guard {
			up := false
			for _, l := range f.links {
				for _, flag := range l.Flags {
					if flag == "UP" {
						up = true
					}
				}
			}
			if !up {
				return nil, errors.New("Device for nexthop is not up")
			}
		}
		dst := args[3]
		if guard {
			dst = args[4]
		}
		if args[2] == "del" {
			var keep []route
			for _, r := range f.routes {
				if r.Destination != dst {
					keep = append(keep, r)
				}
			}
			f.routes = keep
			break
		}
		r := route{Destination: dst, Protocol: json.RawMessage(`242`), Metric: 10, Device: "k0n0"}
		if guard {
			r.Type = "blackhole"
			r.Device = ""
			r.Metric = 32760
		}
		f.routes = append(f.routes, r)
	default:
		return nil, errors.New("unhandled mutation: " + s)
	}
	return nil, nil
}
func fixture(t *testing.T) (*Driver, *fakeKernel) {
	t.Helper()
	c, e := model.Load("../../../examples/inventory.json")
	if e != nil {
		t.Fatal(e)
	}
	keyFile := filepath.Join(t.TempDir(), "key")
	if e = os.WriteFile(keyFile, []byte(c.Nodes[0].WireGuard.PublicKey+"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	c.Nodes[0].WireGuard.PrivateKeyFile = keyFile
	f := &fakeKernel{public: c.Nodes[0].WireGuard.PublicKey}
	return &Driver{Config: c, Node: c.Nodes[0], Runner: f}, f
}
func TestReconcileIdempotentAndRemoveOnlyOwnedObjects(t *testing.T) {
	d, f := fixture(t)
	ctx := context.Background()
	if e := d.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	first := jsonBytes(f.routes)
	conf := string(f.config)
	if e := d.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	if string(first) != string(jsonBytes(f.routes)) || conf != string(f.config) || len(f.links) != 1 {
		t.Fatal("repeat apply changed desired state")
	}
	if !strings.Contains(conf, "AllowedIPs = 10.203.0.2/32") || strings.Contains(conf, d.Config.Kubernetes.PodCIDR) {
		t.Fatal("driver took ownership of Pod routes")
	}
	if e := d.Remove(ctx); e != nil {
		t.Fatal(e)
	}
	if len(f.links) != 0 || len(f.routes) != 0 {
		t.Fatal("owned resources leaked")
	}
	if e := d.Remove(ctx); e != nil {
		t.Fatal(e)
	}
}
func TestPreflightRejectsForeignStateBeforeMutating(t *testing.T) {
	for _, kind := range []string{"interface", "route", "policy", "key"} {
		t.Run(kind, func(t *testing.T) {
			d, f := fixture(t)
			switch kind {
			case "interface":
				f.links = []link{{Name: "k0n0", Alias: "somebody-else"}}
			case "route":
				f.routes = []route{{Destination: "10.0.0.0/8", Device: "eth0"}}
			case "policy":
				f.customRule = true
			case "key":
				f.public = d.Config.Nodes[1].WireGuard.PublicKey
			}
			if e := d.Reconcile(context.Background()); e == nil {
				t.Fatal("unsafe state accepted")
			}
			if f.mutations != 0 {
				t.Fatal("preflight mutated system")
			}
		})
	}
}
func TestFailureRollsBackFirstInstallation(t *testing.T) {
	for _, failure := range []string{"link set dev k0n0 alias k0nnect:v1:lab:control-plane", "wg", "-4 address add 10.203.0.1/32 dev k0n0", "-4 route add 10.203.0.2/32 dev k0n0 proto 242 metric 10", "link set dev k0n0 up"} {
		t.Run(failure, func(t *testing.T) {
			d, f := fixture(t)
			f.fail = failure
			if failure == "wg" {
				f.fail = "syncconf k0n0 /dev/stdin"
			}
			if e := d.Reconcile(context.Background()); e == nil {
				t.Fatal("failure not reported")
			}
			if len(f.links) != 0 || len(f.routes) != 0 || f.address {
				t.Fatalf("rollback leaked resources: %+v", f)
			}
		})
	}
}
func TestFailedUpdateRestoresExistingTransport(t *testing.T) {
	d, f := fixture(t)
	ctx := context.Background()
	if e := d.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	beforeRoutes := string(jsonBytes(f.routes))
	beforeConfig := string(f.config)
	d.Config.Nodes[1].Address = "10.203.0.3"
	f.fail = "-4 route add 10.203.0.3/32 dev k0n0 proto 242 metric 10"
	if e := d.Reconcile(ctx); e == nil {
		t.Fatal("failure not reported")
	}
	if beforeRoutes != string(jsonBytes(f.routes)) || beforeConfig != string(f.config) {
		t.Fatal("existing network was not restored")
	}
}
func TestPeerRemovalRemovesRoutesAndKeys(t *testing.T) {
	d, f := fixture(t)
	ctx := context.Background()
	third := d.Config.Nodes[1]
	third.Name = "third"
	third.Address = "10.203.0.3"
	third.Endpoint = "198.51.100.30:51820"
	third.WireGuard.PublicKey = "AwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwM="
	d.Config.Nodes = append(d.Config.Nodes, third)
	if e := d.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	d.Config.Nodes = d.Config.Nodes[:2]
	if e := d.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	for _, r := range f.routes {
		if r.Destination == "10.203.0.3/32" {
			t.Fatal("revoked route remained")
		}
	}
	if strings.Contains(string(f.config), third.WireGuard.PublicKey) {
		t.Fatal("revoked peer remained")
	}
}
func TestPrivateKeyRejectsPermissionsAndSymlink(t *testing.T) {
	d, _ := fixture(t)
	key := d.Node.WireGuard.PrivateKeyFile
	if e := os.Chmod(key, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e := ReadPrivateKey(key); e == nil {
		t.Fatal("world readable private key accepted")
	}
	if e := os.Chmod(key, 0600); e != nil {
		t.Fatal(e)
	}
	symlink := filepath.Join(t.TempDir(), "link")
	if e := os.Symlink(key, symlink); e != nil {
		t.Fatal(e)
	}
	if _, e := ReadPrivateKey(symlink); e == nil {
		t.Fatal("symlink accepted")
	}
}
func TestCapabilitiesExplicitlyRejectIPv6AndRelay(t *testing.T) {
	d, _ := fixture(t)
	if !reflect.DeepEqual(d.Capabilities(), transport.Capabilities{IPv4: true, Encrypted: true}) {
		t.Fatal("unexpected capabilities")
	}
}
