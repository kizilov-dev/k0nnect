package enroll

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
)

var versionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+\+k0s\.[0-9]+$`)

func base64Decode(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }
func prepareScript(c Config) string {
	marker, _ := json.Marshal(map[string]any{"name": c.Name, "nodeName": c.NodeName, "clusterID": c.ClusterID, "workerAddress": c.WorkerAddress, "transportCIDR": c.TransportCIDR, "port": c.Port, "mtu": c.MTU})
	return `if test -e /etc/k8nnect/managed.json; then echo 'Legacy worker installation: disconnect with the old binary first' >&2; exit 1; fi
if test -e /etc/k0nnect/managed.json; then
	 python3 -c ` + Quote("import json; assert json.load(open('/etc/k0nnect/managed.json'))==json.loads("+fmt.Sprintf("%q", string(marker))+")") + `
else
 if test -e /var/lib/k0s || test -e /run/netns/k0nnect || command -v k0s >/dev/null; then echo 'Existing unmanaged Kubernetes installation or namespace' >&2; exit 1; fi
fi
test "$(uname -m)" = x86_64
if ! command -v wg >/dev/null || ! command -v iptables >/dev/null || ! command -v curl >/dev/null || ! command -v python3 >/dev/null; then
 # A fresh cloud VDS runs cloud-init and the first apt-daily/unattended-upgrades
 # pass right after boot; wait for their dpkg locks instead of failing.
 if command -v cloud-init >/dev/null; then cloud-init status --wait >/dev/null 2>&1 || true; fi
 # Provider package mirrors refuse connections intermittently; retry the whole step.
 for attempt in 1 2 3 4 5; do
  if apt-get -o DPkg::Lock::Timeout=600 update -qq && DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=600 install -y --no-install-recommends wireguard-tools iproute2 iptables curl ca-certificates python3; then break; fi
  if test "$attempt" = 5; then exit 100; fi
  sleep 15
 done
fi
install -d -m 0700 /etc/k0nnect
umask 077
printf '%s\n' ` + Quote(string(marker)) + ` > /etc/k0nnect/managed.json
if ! test -f /etc/k0nnect/worker.key; then wg genkey > /etc/k0nnect/worker.key; fi
if ! test -f /etc/k0nnect/resolv.conf; then
 if test -f /run/systemd/resolve/resolv.conf; then awk '/^nameserver/ && $2 !~ /^127\./ { print }' /run/systemd/resolve/resolv.conf > /etc/k0nnect/resolv.conf; fi
 if ! test -s /etc/k0nnect/resolv.conf; then printf 'nameserver 1.1.1.1\n' > /etc/k0nnect/resolv.conf; fi
fi
`
}
func networkUnitsScript() string {
	return `cat > /etc/systemd/system/k0nnect-network.service <<'UNIT'
[Unit]
Description=k0nnect isolated external Worker network
Wants=network-online.target
After=network-online.target
Before=k0sworker.service
[Service]
Type=oneshot
ExecStart=/usr/local/bin/k0nnect worker-network --config /etc/k0nnect/worker.json
RemainAfterExit=yes
[Install]
WantedBy=multi-user.target
UNIT
cat > /etc/systemd/system/k0nnect-agent.service <<'UNIT'
[Unit]
Description=k0nnect external Worker network reconciler
Requires=k0nnect-network.service
After=k0nnect-network.service
[Service]
ExecStart=/usr/local/bin/k0nnect worker-agent --config /etc/k0nnect/worker.json
Restart=always
RestartSec=5
[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable k0nnect-network.service k0nnect-agent.service
/usr/local/bin/k0nnect worker-network --config /etc/k0nnect/worker.json
systemctl start k0nnect-network.service k0nnect-agent.service
`
}

// Reference CNI plugins pinned by digest. Flannel delegates to bridge,
// host-local and portmap, containerd needs loopback, and Flannel and Cilium
// DaemonSets do not install them: managed clusters preinstall them on their own
// nodes. Plugins already provided by the CNI are never replaced.
const cniPluginsVersion = "v1.9.1"
const cniPluginsSHA256 = "b98f74a0f8522f0a83867178729c1aa70f2158f90c45a2ca8fa791db1c76b303"

var cniPluginsScript = `if ! test -x /opt/cni/bin/loopback || ! test -x /opt/cni/bin/bridge || ! test -x /opt/cni/bin/host-local || ! test -x /opt/cni/bin/portmap; then
 cd /etc/k0nnect
 curl -fsSL --retry 3 ` + Quote("https://github.com/containernetworking/plugins/releases/download/"+cniPluginsVersion+"/cni-plugins-linux-amd64-"+cniPluginsVersion+".tgz") + ` -o cni-plugins.tgz
 echo '` + cniPluginsSHA256 + `  cni-plugins.tgz' | sha256sum -c -
 plugins=$(mktemp -d)
 tar -xzf cni-plugins.tgz -C "$plugins"
 install -d -m 0755 /opt/cni/bin
 for plugin in "$plugins"/*; do
  name=$(basename "$plugin")
  if test -f "$plugin" && test -x "$plugin" && ! test -e "/opt/cni/bin/$name"; then install -m 0755 "$plugin" "/opt/cni/bin/$name"; fi
 done
 rm -rf -- "$plugins" cni-plugins.tgz
fi`

func installScript(c Config, profile string) string {
	asset := "k0s-" + c.K0sVersion + "-amd64"
	base := "https://github.com/k0sproject/k0s/releases/download/" + c.K0sVersion + "/"
	return `if ! test -x /usr/local/bin/k0s || ! /usr/local/bin/k0s version | grep -Fxq ` + Quote(c.K0sVersion) + `; then
 cd /etc/k0nnect
 curl -fsSL --retry 3 ` + Quote(base+asset) + ` -o ` + Quote(asset) + `
 curl -fsSL --retry 3 ` + Quote(base+"sha256sums.txt") + ` -o sha256sums.txt
 awk -v target=` + Quote(asset) + ` '$2 == target || $2 == "*" target { print }' sha256sums.txt > selected.sha256
 test -s selected.sha256
 sha256sum -c selected.sha256
 install -m 0755 ` + Quote(asset) + ` /usr/local/bin/k0s
 rm -- ` + Quote(asset) + `
fi
` + cniPluginsScript + `
/usr/local/bin/k0s install worker --force --token-file=/etc/k0nnect/join.token --profile=` + Quote(profile) + ` --labels=` + Quote("k0nnect.io/external=true,k0nnect.io/connection="+c.Name) + ` --taints=k0nnect.io/initializing=true:NoSchedule --kubelet-extra-args=` + Quote("--node-ip="+c.WorkerAddress+" --hostname-override="+c.NodeName) + `
install -d /etc/systemd/system/k0sworker.service.d
cat > /etc/systemd/system/k0sworker.service.d/10-k0nnect.conf <<'UNIT'
[Unit]
Requires=k0nnect-network.service
After=k0nnect-network.service
[Service]
NetworkNamespacePath=/run/netns/k0nnect
BindReadOnlyPaths=/etc/k0nnect/resolv.conf:/etc/resolv.conf
UNIT
systemctl daemon-reload
systemctl enable k0sworker.service
systemctl restart k0sworker.service
`
}
