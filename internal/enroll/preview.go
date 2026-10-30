package enroll

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"k8nnect/internal/kube"
)

type plannedRequest struct {
	Method string      `json:"method"`
	Path   string      `json:"path"`
	Object kube.Object `json:"object"`
}

// previewTransport forwards GET only. Every mutation is captured locally and
// answered from an in-memory overlay; it cannot modify the real API server.
type previewTransport struct {
	read     http.RoundTripper
	objects  map[string]kube.Object
	requests []plannedRequest
}

func (p *previewTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	respond := func(code int, obj kube.Object) *http.Response {
		b, _ := json.Marshal(obj)
		return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(b)), Request: req}
	}
	if req.Method == "GET" {
		if obj, ok := p.objects[req.URL.Path]; ok {
			return respond(200, obj), nil
		}
		return p.read.RoundTrip(req)
	}
	var obj kube.Object
	if e := json.NewDecoder(req.Body).Decode(&obj); e != nil {
		return nil, e
	}
	path := req.URL.Path
	if req.Method == "POST" {
		path += "/" + kube.Str(kube.Map(obj, "metadata"), "name")
	}
	p.objects[path] = obj
	raw, _ := json.Marshal(obj)
	var redacted kube.Object
	json.Unmarshal(raw, &redacted)
	if kube.Str(redacted, "kind") == "Secret" {
		for _, field := range []string{"data", "stringData"} {
			if data := kube.Map(redacted, field); data != nil {
				for key := range data {
					data[key] = "<redacted>"
				}
			}
		}
	}
	if data := kube.Map(redacted, "binaryData"); data != nil {
		for key := range data {
			data[key] = "<binary payload omitted; generated from the reviewed executable>"
		}
	}
	p.requests = append(p.requests, plannedRequest{Method: req.Method, Path: req.URL.Path, Object: redacted})
	return respond(200, obj), nil
}
func Preview(ctx context.Context, k *kube.Client, c Config, path string, out io.Writer) error {
	c.ClusterID = clusterIdentity(k.CA)
	if e := c.Validate(); e != nil {
		return e
	}
	peers, source, minor, e := discover(ctx, k, &c)
	if e != nil {
		return e
	}
	shadow := *k
	transport := &previewTransport{read: k.HTTP.Transport, objects: map[string]kube.Object{}}
	httpClient := *k.HTTP
	httpClient.Transport = transport
	shadow.HTTP = &httpClient
	if e = InstallAddon(ctx, &shadow, c, io.Discard); e != nil {
		return e
	}
	placeholder := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	nets := map[string]Network{}
	names := []string{}
	for _, p := range peers {
		names = append(names, p.Name)
		nets[p.Name] = Network{Name: c.Name, NodeName: p.Name, CIDR: c.TransportCIDR, Address: p.Address, WorkerAddress: c.WorkerAddress, WorkerEndpoint: fmt.Sprintf("%s:%d", c.Host, c.Port+1), WorkerKey: placeholder, Interface: GatewayInterface, Port: c.Port, MTU: c.MTU}
	}
	if e = gatewayResources(ctx, &shadow, c, nets, names); e != nil {
		return e
	}
	if _, e = workerProfile(ctx, &shadow, c, source, minor); e != nil {
		return e
	}
	result := kube.Object{"previewOnly": true, "secretsRedacted": true, "realMutations": 0, "connection": c.Name, "namespace": c.Namespace, "externalNode": c.NodeName, "vds": c.Host, "gatewayNodes": names, "workerAddress": c.WorkerAddress, "controllerAddresses": kube.Str(kube.Map(source, "data"), "apiServerAddresses"), "bootstrapSecret": tokenName(c), "bootstrapLifetime": "1h; revoked after join or a failed attempt", "kubernetesRequests": transport.requests, "remotePreparation": prepareScript(c), "remoteNetworkUnits": networkUnitsScript(), "remoteWorkerInstallation": installScript(c, "k8nnect-"+c.Name), "notes": []string{"WireGuard public keys are illustrative placeholders until generated on their owning hosts", "Binary ConfigMap payloads and SSH Secret values are redacted: this is a review artifact, not a kubectl apply manifest", "Existing Calico resources and node addresses are not patched", "Installer remains on existing workers; the new worker starts with an initialization taint until scheduler, TCP/UDP, DNS, logs and exec verification succeeds"}}
	b, e := json.MarshalIndent(result, "", "  ")
	if e != nil {
		return e
	}
	// Check redaction before persisting the review artifact.
	for _, credential := range []string{c.PasswordFile, c.PrivateKeyFile} {
		if credential != "" {
			secret, err := os.ReadFile(credential)
			if err != nil {
				return err
			}
			value := strings.TrimSpace(string(secret))
			if value != "" && bytes.Contains(b, []byte(value)) {
				return fmt.Errorf("refusing to persist a review artifact containing credentials")
			}
		}
	}
	if e = os.WriteFile(path, append(b, '\n'), 0600); e != nil {
		return e
	}
	fmt.Fprintln(out, "Read-only plan saved to", path, "(no SSH or Kubernetes mutations)")
	return nil
}
