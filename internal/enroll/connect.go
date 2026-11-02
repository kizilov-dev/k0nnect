package enroll

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"k0nnect/internal/kube"
)

func pause(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
func nodesReady(n kube.Object) bool {
	for _, c := range kube.Items(kube.Map(n, "status"), "conditions") {
		o := kube.As(c)
		if kube.Str(o, "type") == "Ready" && kube.Str(o, "status") == "True" {
			return true
		}
	}
	return false
}
func address(n kube.Object, kind string) string {
	for _, v := range kube.Items(kube.Map(n, "status"), "addresses") {
		a := kube.As(v)
		if kube.Str(a, "type") == kind {
			return kube.Str(a, "address")
		}
	}
	return ""
}

func discover(ctx context.Context, k *kube.Client, c *Config) ([]Peer, kube.Object, string, error) {
	nodes, e := k.Get(ctx, "/api/v1/nodes")
	if e != nil {
		return nil, nil, "", e
	}
	var peers []Peer
	minor := ""
	network := netip.MustParsePrefix(c.TransportCIDR)
	for _, v := range kube.Items(nodes, "items") {
		n := kube.As(v)
		meta := kube.Map(n, "metadata")
		labels := kube.Map(meta, "labels")
		name := kube.Str(meta, "name")
		if name == c.NodeName || kube.Str(labels, "k0nnect.io/external") == "true" || !nodesReady(n) {
			continue
		}
		info := kube.Map(kube.Map(n, "status"), "nodeInfo")
		ver := kube.Str(info, "kubeletVersion")
		if !strings.Contains(ver, "+k0s") || kube.Str(info, "architecture") != "amd64" {
			return nil, nil, "", fmt.Errorf("initial connector requires Linux/amd64 k0s workers")
		}
		parts := strings.Split(strings.TrimPrefix(strings.Split(ver, "+")[0], "v"), ".")
		if len(parts) < 2 {
			return nil, nil, "", fmt.Errorf("invalid kubelet version")
		}
		m := parts[0] + "." + parts[1]
		if minor != "" && minor != m {
			return nil, nil, "", fmt.Errorf("mixed Kubernetes minor versions unsupported")
		}
		minor = m
		internal, external := address(n, "InternalIP"), address(n, "ExternalIP")
		if internal == "" {
			return nil, nil, "", fmt.Errorf("gateway %s needs InternalIP", name)
		}
		a, e := netip.ParseAddr(internal)
		if e != nil || network.Contains(a) {
			return nil, nil, "", fmt.Errorf("transport CIDR overlaps node address")
		}
		endpoint := ""
		if external != "" {
			endpoint = fmt.Sprintf("%s:%d", external, c.Port)
		}
		peers = append(peers, Peer{Name: name, InternalIP: internal, Endpoint: endpoint})
	}
	if len(peers) == 0 || len(peers) > 99 {
		return nil, nil, "", fmt.Errorf("need 1..99 ready gateway workers")
	}
	sort.Slice(peers, func(i, j int) bool {
		if (peers[i].Endpoint != "") != (peers[j].Endpoint != "") {
			return peers[i].Endpoint != ""
		}
		return peers[i].Name < peers[j].Name
	})
	base := network.Addr().As4()
	for i := range peers {
		a := base
		a[3] = byte(i + 1)
		peers[i].Address = netip.AddrFrom4(a).String()
	}
	cni, e := detectCNI(ctx, k, network)
	if e != nil {
		return nil, nil, "", e
	}
	c.CNI, c.GatewayHostPortOnly = cni.Name, cni.HostPortOnly
	services, e := k.Get(ctx, "/api/v1/services")
	if e != nil {
		return nil, nil, "", e
	}
	for _, v := range kube.Items(services, "items") {
		ip := kube.Str(kube.Map(kube.As(v), "spec"), "clusterIP")
		if a, e := netip.ParseAddr(ip); e == nil && network.Contains(a) {
			return nil, nil, "", fmt.Errorf("transport CIDR overlaps Service IP")
		}
	}
	profile, e := k.Get(ctx, "/api/v1/namespaces/kube-system/configmaps/worker-config-default-"+minor)
	if e != nil {
		return nil, nil, "", e
	}
	if c.K0sVersion == "" {
		v := kube.Str(kube.Map(kube.Map(profile, "metadata"), "labels"), "app.kubernetes.io/version")
		c.K0sVersion = strings.Replace(v, "-k0s.", "+k0s.", 1)
	}
	if !versionPattern.MatchString(c.K0sVersion) {
		return nil, nil, "", fmt.Errorf("cannot determine supported k0s release")
	}
	return peers, profile, minor, nil
}

func Connect(ctx context.Context, k *kube.Client, c Config, out io.Writer) error {
	c.ClusterID = clusterIdentity(k.CA)
	if e := c.Validate(); e != nil {
		return e
	}
	if e := rejectLegacyInstallation(ctx, k); e != nil {
		return e
	}
	log := func(s string) { fmt.Fprintln(out, s) }
	log("Discovering k0s and CNI compatibility")
	peers, source, minor, e := discover(ctx, k, &c)
	if e != nil {
		return e
	}
	log("Detected CNI: " + c.CNI)
	if existing, err := k.Get(ctx, "/api/v1/nodes/"+c.NodeName); err == nil {
		if kube.Str(kube.Map(kube.Map(existing, "metadata"), "labels"), "k0nnect.io/connection") != c.Name {
			return fmt.Errorf("refusing an existing node not owned by this connection")
		}
		if ip := address(existing, "InternalIP"); ip != "" && ip != c.WorkerAddress {
			return fmt.Errorf("worker address migration requires explicit disconnect; existing InternalIP is %s", ip)
		}
	} else if !kube.Code(err, 404) {
		return err
	}
	log("Authenticating pinned SSH host and preparing VDS")
	r, e := Dial(ctx, c)
	if e != nil {
		return e
	}
	defer r.Close()
	if _, e = r.Run(ctx, prepareScript(c), nil); e != nil {
		return fmt.Errorf("prepare VDS: %w", e)
	}
	binaryPath, e := os.Executable()
	if e != nil {
		return e
	}
	binary, e := os.ReadFile(binaryPath)
	if e != nil {
		return e
	}
	if e = r.Put(ctx, "/usr/local/bin/k0nnect", binary, "0700"); e != nil {
		return e
	}
	pub, e := r.Run(ctx, "wg pubkey < /etc/k0nnect/worker.key", nil)
	if e != nil {
		return e
	}
	workerKey := strings.TrimSpace(string(pub))
	if e = ensureNamespace(ctx, k, c); e != nil {
		return e
	}
	networks := map[string]Network{}
	names := []string{}
	for _, p := range peers {
		names = append(names, p.Name)
		networks[p.Name] = Network{Name: c.Name, NodeName: p.Name, CIDR: c.TransportCIDR, Address: p.Address, WorkerAddress: c.WorkerAddress, WorkerEndpoint: fmt.Sprintf("%s:%d", c.Host, c.Port+1), WorkerKey: workerKey, Interface: GatewayInterface, Port: c.Port, MTU: c.MTU}
	}
	log("Installing owned gateway agents on existing workers")
	if e = gatewayResources(ctx, k, c, networks, names); e != nil {
		return e
	}
	deadline := time.Now().Add(5 * time.Minute)
	nodeMTU := 0
	for {
		ready := true
		nodeMTU = 0
		pods, e := k.Get(ctx, "/api/v1/namespaces/"+c.Namespace+"/pods?labelSelector=app%3Dk0nnect-gateway")
		if e != nil {
			return e
		}
		readyNodes := map[string]bool{}
		for _, v := range kube.Items(pods, "items") {
			pod := kube.As(v)
			for _, x := range kube.Items(kube.Map(pod, "status"), "conditions") {
				s := kube.As(x)
				if kube.Str(s, "type") == "Ready" && kube.Str(s, "status") == "True" {
					readyNodes[kube.Str(kube.Map(pod, "spec"), "nodeName")] = true
				}
			}
		}
		state, e := k.Get(ctx, "/api/v1/namespaces/"+c.Namespace+"/configmaps/"+GatewayState)
		if e != nil {
			return e
		}
		published := kube.Map(state, "data")
		for i := range peers {
			keyField, mtuField := gatewayStateKeys(peers[i].Name)
			peers[i].PublicKey = kube.Str(published, keyField)
			mtu, err := strconv.Atoi(kube.Str(published, mtuField))
			if peers[i].PublicKey == "" || err != nil || !readyNodes[peers[i].Name] {
				ready = false
			} else if nodeMTU == 0 || mtu < nodeMTU {
				nodeMTU = mtu
			}
		}
		if ready {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("gateway agents did not become Ready; inspect gateway Pod logs")
		}
		if e = pause(ctx, 3*time.Second); e != nil {
			return e
		}
	}
	worker := Network{Name: c.Name, NodeName: c.NodeName, CIDR: c.TransportCIDR, Address: c.WorkerAddress, WorkerAddress: c.WorkerAddress, WorkerEndpoint: fmt.Sprintf("%s:%d", c.Host, c.Port+1), WorkerKey: workerKey, Port: c.Port + 1, MTU: c.MTU, Peers: peers}
	if nodeMTU > c.MTU {
		worker.NodeMTU = nodeMTU
	}
	data, e := json.Marshal(worker)
	if e != nil {
		return e
	}
	if e = r.Put(ctx, "/etc/k0nnect/worker.json", data, "0600"); e != nil {
		return e
	}
	log("Starting isolated Worker network and checking controller reachability")
	if _, e = r.Run(ctx, networkUnitsScript(), nil); e != nil {
		return fmt.Errorf("start isolated Worker network: %w", e)
	}
	controllerAddresses := []string{}
	if e = json.Unmarshal([]byte(kube.Str(kube.Map(source, "data"), "apiServerAddresses")), &controllerAddresses); e != nil || len(controllerAddresses) == 0 {
		return fmt.Errorf("missing controller addresses")
	}
	// Reachability is checked inside the exact namespace used by kubelet/containerd.
	host := strings.Split(controllerAddresses[0], ":")[0]
	probe := fmt.Sprintf("ip netns exec k0nnect python3 -c %s", Quote(fmt.Sprintf("import socket; socket.create_connection((%q,6443),10).close(); socket.create_connection((%q,8132),10).close(); print('controller paths reachable')", host, host)))
	if _, e = r.Run(ctx, probe, nil); e != nil {
		return fmt.Errorf("controller reachability over gateway failed: %w", e)
	}
	existing, e := k.Get(ctx, "/api/v1/nodes/"+c.NodeName)
	// Node readiness lags behind a stopped kubelet, e.g. after an interrupted
	// disconnect; only a running k0sworker lets the Node state be trusted.
	service, err := r.Run(ctx, "systemctl is-active k0sworker.service || true", nil)
	if err != nil {
		return err
	}
	if e == nil && nodesReady(existing) && strings.TrimSpace(string(service)) == "active" {
		log("Worker is already Ready; network reconciled")
		if kube.Str(kube.Map(kube.Map(existing, "metadata"), "annotations"), "k0nnect.io/verified") == "" {
			return VerifyNode(ctx, k, c, out)
		}
		return nil
	}
	if e != nil && !kube.Code(e, 404) {
		return e
	}
	profile, e := workerProfile(ctx, k, c, source, minor)
	if e != nil {
		return e
	}
	secretName, token, e := issueToken(ctx, k, c)
	if e != nil {
		return e
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := k.Delete(cleanup, "/api/v1/namespaces/kube-system/secrets/"+secretName); err != nil {
			fmt.Fprintln(out, "Bootstrap token cleanup failed; token retains its one-hour expiry")
		}
	}()
	// Only a scoped, expiring bootstrap token is transferred; never administrator credentials.
	if e = r.Put(ctx, "/etc/k0nnect/join.token", []byte(token+"\n"), "0600"); e != nil {
		return e
	}
	log("Installing verified k0s release and registering Worker")
	if _, e = r.Run(ctx, installScript(c, profile), nil); e != nil {
		return fmt.Errorf("install k0s Worker: %w", e)
	}
	deadline = time.Now().Add(8 * time.Minute)
	for {
		if e = approveServing(ctx, k, c); e != nil {
			return e
		}
		n, e := k.Get(ctx, "/api/v1/nodes/"+c.NodeName)
		if e == nil && nodesReady(n) {
			if address(n, "InternalIP") != c.WorkerAddress {
				return fmt.Errorf("registered node has unexpected InternalIP")
			}
			break
		}
		if e != nil && !kube.Code(e, 404) {
			return e
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("Worker did not reach Ready; inspect k0sworker and %s logs", c.CNI)
		}
		if e = pause(ctx, 5*time.Second); e != nil {
			return e
		}
	}
	if e = k.Delete(ctx, "/api/v1/namespaces/kube-system/secrets/"+secretName); e != nil {
		return e
	}
	log("Worker Ready; bootstrap token revoked; verifying workload before activation")
	return VerifyNode(ctx, k, c, out)
}

func clusterIdentity(ca []byte) string {
	block, _ := pem.Decode(ca)
	if block != nil {
		ca = block.Bytes
	}
	sum := sha256.Sum256(ca)
	return fmt.Sprintf("%x", sum)
}
func approveServing(ctx context.Context, k *kube.Client, c Config) error {
	all, e := k.Get(ctx, "/apis/certificates.k8s.io/v1/certificatesigningrequests")
	if e != nil {
		return e
	}
	for _, v := range kube.Items(all, "items") {
		csr := kube.As(v)
		spec := kube.Map(csr, "spec")
		if kube.Str(spec, "username") != "system:node:"+c.NodeName || kube.Str(spec, "signerName") != "kubernetes.io/kubelet-serving" || kube.Str(kube.Map(csr, "status"), "certificate") != "" {
			continue
		}
		approved := false
		for _, cond := range kube.Items(kube.Map(csr, "status"), "conditions") {
			if kube.Str(kube.As(cond), "type") == "Approved" {
				approved = true
			}
		}
		if approved {
			continue
		}
		encoded := kube.Str(spec, "request")
		block, e := decodeCSR(encoded)
		if e != nil {
			return e
		}
		request, e := x509.ParseCertificateRequest(block)
		if e != nil || request.CheckSignature() != nil || request.Subject.CommonName != "system:node:"+c.NodeName {
			return fmt.Errorf("unexpected kubelet serving CSR")
		}
		if len(request.Subject.Organization) != 1 || request.Subject.Organization[0] != "system:nodes" || len(request.EmailAddresses) > 0 || len(request.URIs) > 0 {
			return fmt.Errorf("unexpected kubelet serving CSR identity")
		}
		for _, ip := range request.IPAddresses {
			if ip.String() != c.WorkerAddress {
				return fmt.Errorf("unexpected serving CSR IP")
			}
		}
		for _, name := range request.DNSNames {
			if name != c.NodeName {
				return fmt.Errorf("unexpected serving CSR hostname")
			}
		}
		name := kube.Str(kube.Map(csr, "metadata"), "name")
		csr["status"] = kube.Object{"conditions": []any{kube.Object{"type": "Approved", "status": "True", "reason": "K0nnectValidatedNode", "message": "Validated external node identity and SANs", "lastUpdateTime": time.Now().UTC().Format(time.RFC3339)}}}
		if _, e = k.Do(ctx, "PUT", "/apis/certificates.k8s.io/v1/certificatesigningrequests/"+url.PathEscape(name)+"/approval", csr); e != nil {
			return e
		}
	}
	return nil
}
func decodeCSR(encoded string) ([]byte, error) {
	b, e := base64Decode(encoded)
	if e != nil {
		return nil, e
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, fmt.Errorf("invalid CSR PEM")
	}
	return block.Bytes, nil
}
