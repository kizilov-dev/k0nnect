package enroll

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"k8nnect/internal/kube"
)

// Supported CNI profiles. Each one must select the Worker transport address by
// the interface name eth1, which is the WireGuard interface inside the VDS
// Worker namespace, and must encapsulate Pod traffic between node addresses.
const (
	CNICalico     = "calico"
	CNIFlannel    = "flannel"
	CNICilium     = "cilium"
	CNIKubeRouter = "kube-router"
)

// cniConfigMaps are the only CNI configuration objects discovery reads.
var cniConfigMaps = []string{"kube-flannel-cfg", "cilium-config"}

// cniProfile is what enrollment needs to know about the detected CNI.
type cniProfile struct {
	Name string
	// HostPortOnly is set when the CNI rejects host sockets on ports of NodePort
	// Services, so the gateway WireGuard port must not be published as one.
	HostPortOnly bool
}

type cniDaemonSet struct {
	name, namespace, daemonSet string
}

// The first DaemonSet found decides the profile; the locations are those of the
// upstream manifests and of the k0s-managed kube-router.
var cniDaemonSets = []cniDaemonSet{
	{CNICalico, "calico-system", "calico-node"},
	{CNICalico, "kube-system", "calico-node"},
	{CNICilium, "kube-system", "cilium"},
	{CNIFlannel, "kube-flannel", "kube-flannel-ds"},
	{CNIFlannel, "kube-system", "kube-flannel-ds"},
	{CNIKubeRouter, "kube-system", "kube-router"},
}

// detectCNI identifies the cluster CNI and rejects configurations whose Pod
// datapath cannot cross the routed WireGuard transport unchanged.
func detectCNI(ctx context.Context, k *kube.Client, network netip.Prefix) (cniProfile, error) {
	for _, d := range cniDaemonSets {
		ds, e := k.Get(ctx, "/apis/apps/v1/namespaces/"+d.namespace+"/daemonsets/"+d.daemonSet)
		if kube.Code(e, 404) {
			continue
		}
		if e != nil {
			return cniProfile{}, e
		}
		profile := cniProfile{Name: d.name}
		switch d.name {
		case CNICalico:
			e = checkCalico(ctx, k, network, ds)
		case CNIFlannel:
			e = checkFlannel(ctx, k, network, d.namespace, ds)
		case CNICilium:
			profile.HostPortOnly, e = checkCilium(ctx, k, network)
		case CNIKubeRouter:
			if e = checkKubeRouter(ds); e == nil {
				e = checkNodePodCIDRs(ctx, k, network, "kube-router")
			}
		}
		return profile, e
	}
	return cniProfile{}, fmt.Errorf("no supported CNI found: expected Calico, Flannel, Cilium or kube-router DaemonSet")
}

func containerEnv(ds kube.Object, name string) string {
	value := ""
	for _, container := range kube.Items(kube.Map(kube.Map(kube.Map(ds, "spec"), "template"), "spec"), "containers") {
		for _, v := range kube.Items(kube.As(container), "env") {
			env := kube.As(v)
			if kube.Str(env, "name") == name {
				value = kube.Str(env, "value")
			}
		}
	}
	return value
}

// containerFlags collects --name=value and --name value arguments of the first container.
func containerFlags(ds kube.Object) map[string]string {
	flags := map[string]string{}
	containers := kube.Items(kube.Map(kube.Map(kube.Map(ds, "spec"), "template"), "spec"), "containers")
	if len(containers) == 0 {
		return flags
	}
	c := kube.As(containers[0])
	var words []string
	for _, field := range []string{"command", "args"} {
		for _, v := range kube.Items(c, field) {
			if s, ok := v.(string); ok {
				words = append(words, s)
			}
		}
	}
	for i, w := range words {
		if !strings.HasPrefix(w, "--") {
			continue
		}
		name, value, found := strings.Cut(strings.TrimPrefix(w, "--"), "=")
		if !found {
			value = "true"
			if i+1 < len(words) && !strings.HasPrefix(words[i+1], "-") {
				value = words[i+1]
			}
		}
		flags[name] = value
	}
	return flags
}

func checkPodNetwork(cidr string, network netip.Prefix, what string) error {
	p, e := netip.ParsePrefix(cidr)
	if e != nil || !p.Addr().Is4() {
		return fmt.Errorf("%s requires an IPv4 Pod network; found %q", what, cidr)
	}
	if p.Overlaps(network) {
		return fmt.Errorf("transport CIDR overlaps %s Pod network %s", what, cidr)
	}
	return nil
}

// checkNodePodCIDRs validates CNIs whose Pod subnets are the Nodes' podCIDRs.
func checkNodePodCIDRs(ctx context.Context, k *kube.Client, network netip.Prefix, what string) error {
	nodes, e := k.Get(ctx, "/api/v1/nodes")
	if e != nil {
		return e
	}
	for _, v := range kube.Items(nodes, "items") {
		spec := kube.Map(kube.As(v), "spec")
		cidrs := kube.Items(spec, "podCIDRs")
		if len(cidrs) == 0 && kube.Str(spec, "podCIDR") != "" {
			cidrs = []any{kube.Str(spec, "podCIDR")}
		}
		for _, c := range cidrs {
			if p, err := netip.ParsePrefix(fmt.Sprint(c)); err == nil && !p.Addr().Is4() {
				continue
			}
			if e = checkPodNetwork(fmt.Sprint(c), network, what); e != nil {
				return e
			}
		}
	}
	return nil
}

func checkCalico(ctx context.Context, k *kube.Client, network netip.Prefix, ds kube.Object) error {
	pools, e := k.Get(ctx, "/apis/crd.projectcalico.org/v1/ippools")
	if e != nil {
		return fmt.Errorf("Calico IPPool discovery: %w", e)
	}
	if len(kube.Items(pools, "items")) == 0 {
		return fmt.Errorf("no Calico IPPools")
	}
	for _, v := range kube.Items(pools, "items") {
		spec := kube.Map(kube.As(v), "spec")
		if e := checkPodNetwork(kube.Str(spec, "cidr"), network, "Calico"); e != nil {
			return e
		}
		if kube.Str(spec, "vxlanMode") != "Always" || kube.Str(spec, "ipipMode") != "Never" {
			return fmt.Errorf("requires Calico IPv4 VXLAN Always pools")
		}
	}
	if method := containerEnv(ds, "IP_AUTODETECTION_METHOD"); method != "interface=eth1" {
		return fmt.Errorf("managed Calico adapter requires IP autodetection interface=eth1; found %q", method)
	}
	return nil
}

func checkFlannel(ctx context.Context, k *kube.Client, network netip.Prefix, namespace string, ds kube.Object) error {
	cm, e := k.Get(ctx, "/api/v1/namespaces/"+namespace+"/configmaps/kube-flannel-cfg")
	if e != nil {
		return fmt.Errorf("Flannel configuration discovery: %w", e)
	}
	var conf struct {
		Network string
		Backend struct {
			Type          string
			DirectRouting bool
		}
	}
	if e = json.Unmarshal([]byte(kube.Str(kube.Map(cm, "data"), "net-conf.json")), &conf); e != nil {
		return fmt.Errorf("invalid Flannel net-conf.json: %w", e)
	}
	if e = checkPodNetwork(conf.Network, network, "Flannel"); e != nil {
		return e
	}
	// DirectRouting would replace VXLAN by host routes to node addresses that the
	// external Worker cannot be on-link with.
	if !strings.EqualFold(conf.Backend.Type, "vxlan") || conf.Backend.DirectRouting {
		return fmt.Errorf("requires Flannel VXLAN backend without DirectRouting; found %q", conf.Backend.Type)
	}
	flags := containerFlags(ds)
	if flags["kube-subnet-mgr"] != "true" {
		return fmt.Errorf("requires Flannel --kube-subnet-mgr")
	}
	if iface := flags["iface"]; iface != "" && iface != "eth1" || flags["iface-regex"] != "" || flags["iface-can-reach"] != "" {
		return fmt.Errorf("Flannel must select interface eth1 or the default route interface")
	}
	if flags["public-ip"] != "" {
		return fmt.Errorf("Flannel --public-ip is not supported for an external Worker")
	}
	return nil
}

func checkCilium(ctx context.Context, k *kube.Client, network netip.Prefix) (bool, error) {
	cm, e := k.Get(ctx, "/api/v1/namespaces/kube-system/configmaps/cilium-config")
	if e != nil {
		return false, fmt.Errorf("Cilium configuration discovery: %w", e)
	}
	data := kube.Map(cm, "data")
	mode := kube.Str(data, "routing-mode")
	if mode == "" {
		// Releases before 1.14 configured the datapath with "tunnel" alone.
		if t := kube.Str(data, "tunnel"); t != "disabled" {
			mode = "tunnel"
		}
	}
	if mode != "tunnel" {
		return false, fmt.Errorf("requires Cilium tunnel routing mode; found %q", mode)
	}
	if p := kube.Str(data, "tunnel-protocol"); p != "" && p != "vxlan" && p != "geneve" {
		return false, fmt.Errorf("unsupported Cilium tunnel protocol %q", p)
	}
	if kube.Str(data, "enable-ipv4") != "true" {
		return false, fmt.Errorf("requires Cilium IPv4")
	}
	ipam := kube.Str(data, "ipam")
	if ipam != "cluster-pool" && ipam != "kubernetes" {
		return false, fmt.Errorf("unsupported Cilium IPAM mode %q", ipam)
	}
	if ipam == "kubernetes" {
		if e = checkNodePodCIDRs(ctx, k, network, "Cilium"); e != nil {
			return false, e
		}
	}
	if cidr := kube.Str(data, "cluster-pool-ipv4-cidr"); cidr != "" {
		for _, c := range strings.Fields(strings.ReplaceAll(cidr, ",", " ")) {
			if e = checkPodNetwork(c, network, "Cilium"); e != nil {
				return false, e
			}
		}
	}
	for _, d := range strings.Fields(strings.ReplaceAll(kube.Str(data, "devices"), ",", " ")) {
		if d != "eth1" && d != "eth+" {
			return false, fmt.Errorf("Cilium devices must include only eth1; found %q", kube.Str(data, "devices"))
		}
	}
	if kube.Str(data, "enable-wireguard") == "true" || kube.Str(data, "enable-ipsec") == "true" {
		return false, fmt.Errorf("Cilium transparent encryption is not supported for an external Worker")
	}
	// With kube-proxy replacement Cilium refuses bind(2) to NodePort Service
	// ports on every node, including the WireGuard socket of a gateway.
	kpr := kube.Str(data, "kube-proxy-replacement")
	return (kpr == "true" || kpr == "strict") && kube.Str(data, "node-port-bind-protection") != "false", nil
}

// cniSettleTime bounds how long the CNI on existing nodes may take to program a
// newly joined node. kube-router adds a new node's Pod subnet and address to the
// ipsets that exempt Pod traffic from masquerading only on its periodic route
// sync; until then replies from the external Worker's Pods are dropped.
func cniSettleTime(ctx context.Context, k *kube.Client) time.Duration {
	ds, e := k.Get(ctx, "/apis/apps/v1/namespaces/kube-system/daemonsets/kube-router")
	if e != nil {
		return time.Minute
	}
	period := 5 * time.Minute
	if d, err := time.ParseDuration(containerFlags(ds)["routes-sync-period"]); err == nil && d > 0 {
		period = d
	}
	return period + time.Minute
}

func checkKubeRouter(ds kube.Object) error {
	flags := containerFlags(ds)
	if flags["run-router"] == "false" {
		return fmt.Errorf("requires kube-router --run-router")
	}
	if flags["enable-ipv4"] == "false" {
		return fmt.Errorf("requires kube-router IPv4")
	}
	// The external Worker is never on a node subnet, so routes to its Pods must
	// use the kube-router IPIP overlay rather than direct next hops.
	if flags["enable-overlay"] == "false" {
		return fmt.Errorf("requires kube-router --enable-overlay")
	}
	if t := flags["overlay-type"]; t != "" && t != "subnet" && t != "full" {
		return fmt.Errorf("unsupported kube-router overlay type %q", t)
	}
	if flags["nodes-full-mesh"] == "false" || flags["peer-router-ips"] != "" {
		return fmt.Errorf("kube-router external BGP peers and route reflectors are not supported")
	}
	return nil
}
