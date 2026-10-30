package enroll

import (
	"context"
	"net/http"
	"net/netip"
	"strings"
	"testing"

	"k0nnect/internal/kube"
)

// Fixtures mirror the DaemonSets and ConfigMaps of managed k0s clusters.
func daemonSet(args []string, env map[string]string) kube.Object {
	vars := []any{}
	for k, v := range env {
		vars = append(vars, kube.Object{"name": k, "value": v})
	}
	words := []any{}
	for _, a := range args {
		words = append(words, a)
	}
	return kube.Object{"spec": kube.Object{"template": kube.Object{"spec": kube.Object{"containers": []any{kube.Object{"name": "main", "args": words, "env": vars}}}}}}
}

func cniCluster(objects map[string]kube.Object) *kube.Client {
	rt := roundTrip(func(r *http.Request) (*http.Response, error) {
		if obj, ok := objects[r.URL.Path]; ok {
			return response(200, obj), nil
		}
		if r.URL.Path == nodesPath {
			return response(200, kube.Object{"items": []any{}}), nil
		}
		return response(404, kube.Object{}), nil
	})
	return &kube.Client{Server: "https://example.test", HTTP: &http.Client{Transport: rt}}
}

const (
	calicoDS     = "/apis/apps/v1/namespaces/calico-system/daemonsets/calico-node"
	calicoPools  = "/apis/crd.projectcalico.org/v1/ippools"
	flannelDS    = "/apis/apps/v1/namespaces/kube-system/daemonsets/kube-flannel-ds"
	flannelCfg   = "/api/v1/namespaces/kube-system/configmaps/kube-flannel-cfg"
	ciliumDS     = "/apis/apps/v1/namespaces/kube-system/daemonsets/cilium"
	ciliumCfg    = "/api/v1/namespaces/kube-system/configmaps/cilium-config"
	kubeRouterDS = "/apis/apps/v1/namespaces/kube-system/daemonsets/kube-router"
)

const nodesPath = "/api/v1/nodes"

func nodePodCIDRs(cidrs ...string) kube.Object {
	items := []any{}
	for _, c := range cidrs {
		items = append(items, kube.Object{"spec": kube.Object{"podCIDR": c, "podCIDRs": []any{c}}})
	}
	return kube.Object{"items": items}
}

func flannelConf(conf string) kube.Object {
	return kube.Object{"data": kube.Object{"net-conf.json": conf}}
}
func ciliumConf(overrides map[string]string) kube.Object {
	data := kube.Object{"routing-mode": "tunnel", "tunnel-protocol": "vxlan", "enable-ipv4": "true", "ipam": "cluster-pool", "cluster-pool-ipv4-cidr": "10.244.0.0/16", "devices": "eth1"}
	for k, v := range overrides {
		data[k] = v
	}
	return kube.Object{"data": data}
}

func TestDetectCNI(t *testing.T) {
	network := netip.MustParsePrefix("10.203.74.0/24")
	flannelArgs := []string{"--ip-masq", "--kube-subnet-mgr", "--iface=eth1"}
	routerArgs := []string{"--run-router=true", "--run-firewall=true", "--enable-ipv4=true"}
	vxlanPool := kube.Object{"items": []any{kube.Object{"spec": kube.Object{"cidr": "10.244.0.0/16", "vxlanMode": "Always", "ipipMode": "Never"}}}}
	cases := []struct {
		name    string
		objects map[string]kube.Object
		want    string
		err     string
	}{
		// Cilium with kube-proxy replacement rejects binds to NodePort ports.
		{"cilium kube-proxy replacement", map[string]kube.Object{ciliumDS: daemonSet(nil, nil), ciliumCfg: ciliumConf(map[string]string{"kube-proxy-replacement": "true"})}, CNICilium + "+hostport", ""},
		{"cilium without bind protection", map[string]kube.Object{ciliumDS: daemonSet(nil, nil), ciliumCfg: ciliumConf(map[string]string{"kube-proxy-replacement": "true", "node-port-bind-protection": "false"})}, CNICilium, ""},
		{"calico", map[string]kube.Object{calicoDS: daemonSet(nil, map[string]string{"IP_AUTODETECTION_METHOD": "interface=eth1"}), calicoPools: vxlanPool}, CNICalico, ""},
		{"calico autodetection", map[string]kube.Object{calicoDS: daemonSet(nil, map[string]string{"IP_AUTODETECTION_METHOD": "first-found"}), calicoPools: vxlanPool}, CNICalico, "interface=eth1"},
		{"calico ipip", map[string]kube.Object{calicoDS: daemonSet(nil, map[string]string{"IP_AUTODETECTION_METHOD": "interface=eth1"}), calicoPools: kube.Object{"items": []any{kube.Object{"spec": kube.Object{"cidr": "10.244.0.0/16", "vxlanMode": "Never", "ipipMode": "Always"}}}}}, CNICalico, "VXLAN Always"},
		{"flannel", map[string]kube.Object{flannelDS: daemonSet(flannelArgs, nil), flannelCfg: flannelConf(`{"Network":"10.244.0.0/16","Backend":{"Type":"vxlan"}}`)}, CNIFlannel, ""},
		{"flannel default interface", map[string]kube.Object{flannelDS: daemonSet([]string{"--kube-subnet-mgr"}, nil), flannelCfg: flannelConf(`{"Network":"10.244.0.0/16","Backend":{"Type":"vxlan"}}`)}, CNIFlannel, ""},
		{"flannel host-gw", map[string]kube.Object{flannelDS: daemonSet(flannelArgs, nil), flannelCfg: flannelConf(`{"Network":"10.244.0.0/16","Backend":{"Type":"host-gw"}}`)}, CNIFlannel, "VXLAN"},
		{"flannel direct routing", map[string]kube.Object{flannelDS: daemonSet(flannelArgs, nil), flannelCfg: flannelConf(`{"Network":"10.244.0.0/16","Backend":{"Type":"vxlan","DirectRouting":true}}`)}, CNIFlannel, "DirectRouting"},
		{"flannel other interface", map[string]kube.Object{flannelDS: daemonSet([]string{"--kube-subnet-mgr", "--iface", "ens3"}, nil), flannelCfg: flannelConf(`{"Network":"10.244.0.0/16","Backend":{"Type":"vxlan"}}`)}, CNIFlannel, "eth1"},
		{"flannel overlap", map[string]kube.Object{flannelDS: daemonSet(flannelArgs, nil), flannelCfg: flannelConf(`{"Network":"10.203.0.0/16","Backend":{"Type":"vxlan"}}`)}, CNIFlannel, "overlaps"},
		{"cilium", map[string]kube.Object{ciliumDS: daemonSet(nil, nil), ciliumCfg: ciliumConf(nil)}, CNICilium, ""},
		{"cilium native routing", map[string]kube.Object{ciliumDS: daemonSet(nil, nil), ciliumCfg: ciliumConf(map[string]string{"routing-mode": "native"})}, CNICilium, "tunnel"},
		{"cilium devices", map[string]kube.Object{ciliumDS: daemonSet(nil, nil), ciliumCfg: ciliumConf(map[string]string{"devices": "ens3"})}, CNICilium, "eth1"},
		{"cilium encryption", map[string]kube.Object{ciliumDS: daemonSet(nil, nil), ciliumCfg: ciliumConf(map[string]string{"enable-wireguard": "true"})}, CNICilium, "encryption"},
		{"kube-router", map[string]kube.Object{kubeRouterDS: daemonSet(routerArgs, nil)}, CNIKubeRouter, ""},
		{"kube-router without overlay", map[string]kube.Object{kubeRouterDS: daemonSet(append(routerArgs, "--enable-overlay=false"), nil)}, CNIKubeRouter, "overlay"},
		{"kube-router route reflector", map[string]kube.Object{kubeRouterDS: daemonSet(append(routerArgs, "--nodes-full-mesh=false"), nil)}, CNIKubeRouter, "BGP"},
		{"kube-router pod CIDR overlap", map[string]kube.Object{kubeRouterDS: daemonSet(routerArgs, nil), nodesPath: nodePodCIDRs("10.244.0.0/24", "10.203.74.0/24")}, CNIKubeRouter, "overlaps"},
		{"cilium kubernetes IPAM overlap", map[string]kube.Object{ciliumDS: daemonSet(nil, nil), ciliumCfg: ciliumConf(map[string]string{"ipam": "kubernetes", "cluster-pool-ipv4-cidr": ""}), nodesPath: nodePodCIDRs("10.203.0.0/16")}, CNICilium, "overlaps"},
		{"cilium kubernetes IPAM", map[string]kube.Object{ciliumDS: daemonSet(nil, nil), ciliumCfg: ciliumConf(map[string]string{"ipam": "kubernetes", "cluster-pool-ipv4-cidr": ""}), nodesPath: nodePodCIDRs("10.244.0.0/24")}, CNICilium, ""},
		{"unknown", map[string]kube.Object{}, "", "no supported CNI"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			profile, e := detectCNI(context.Background(), cniCluster(tc.objects), network)
			got := profile.Name
			if profile.HostPortOnly {
				got += "+hostport"
			}
			if got != tc.want {
				t.Fatalf("detected %q, want %q", got, tc.want)
			}
			if tc.err == "" && e != nil {
				t.Fatalf("unexpected rejection: %v", e)
			}
			if tc.err != "" && (e == nil || !strings.Contains(e.Error(), tc.err)) {
				t.Fatalf("error %v, want containing %q", e, tc.err)
			}
		})
	}
}

func TestContainerFlags(t *testing.T) {
	flags := containerFlags(daemonSet([]string{"/opt/bin/flanneld", "--ip-masq", "--iface", "eth1", "--healthz-port=8081"}, nil))
	if flags["ip-masq"] != "true" || flags["iface"] != "eth1" || flags["healthz-port"] != "8081" {
		t.Fatalf("unexpected flags %v", flags)
	}
}
