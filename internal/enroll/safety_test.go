package enroll

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k0nnect/internal/kube"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(code int, body kube.Object) *http.Response {
	b, _ := json.Marshal(body)
	return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(b))}
}
func fixtureConfig() Config {
	return Config{Name: "test-vds", NodeName: "external-node", Namespace: "k0nnect-system", Host: "198.51.100.2", HostKeySHA256: "SHA256:012345678901234567890123456789012345678901234", TransportCIDR: "10.203.74.0/24", WorkerAddress: "10.203.74.100", Port: 31820, MTU: 1400, K0sVersion: "v1.36.4+k0s.0"}
}
func TestPreviewNeverForwardsMutationsAndRedactsCredentials(t *testing.T) {
	c := fixtureConfig()
	c.PasswordFile = filepath.Join(t.TempDir(), "password")
	const password = "not-a-real-integration-password"
	if e := os.WriteFile(c.PasswordFile, []byte(password), 0600); e != nil {
		t.Fatal(e)
	}
	reads := 0
	read := roundTrip(func(r *http.Request) (*http.Response, error) {
		reads++
		if r.Method != "GET" {
			t.Fatalf("mutation reached real transport: %s", r.Method)
		}
		return response(404, kube.Object{}), nil
	})
	preview := &previewTransport{read: read, objects: map[string]kube.Object{}}
	k := &kube.Client{Server: "https://example.test", HTTP: &http.Client{Transport: preview}}
	if e := InstallAddon(context.Background(), k, c, io.Discard); e != nil {
		t.Fatal(e)
	}
	if reads == 0 || len(preview.requests) == 0 {
		t.Fatal("preview exercised no operations")
	}
	b, e := json.Marshal(preview.requests)
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(b, []byte(password)) {
		t.Fatal("review plan leaked password")
	}
	if !bytes.Contains(b, []byte("redacted")) {
		t.Fatal("secret was not redacted")
	}
	foundDeployment, foundTokenScope := false, false
	for _, r := range preview.requests {
		if kube.Str(r.Object, "kind") == "Deployment" {
			foundDeployment = true
			spec := kube.Map(kube.Map(kube.Map(r.Object, "spec"), "template"), "spec")
			if spec["hostNetwork"] == true {
				t.Fatal("installer itself has host network")
			}
		}
		if kube.Str(r.Object, "kind") == "Role" && kube.Str(kube.Map(r.Object, "metadata"), "namespace") == "kube-system" {
			for _, v := range kube.Items(r.Object, "rules") {
				rule := kube.As(v)
				raw, _ := json.Marshal(rule)
				if bytes.Contains(raw, []byte(`"delete"`)) && bytes.Contains(raw, []byte(`"secrets"`)) {
					names := kube.Items(rule, "resourceNames")
					if len(names) != 1 || names[0] != tokenName(c) {
						t.Fatal("bootstrap cleanup permissions not scoped")
					}
					foundTokenScope = true
				}
			}
		}
	}
	if !foundDeployment || !foundTokenScope {
		t.Fatal("addon or token permissions missing")
	}
}
func TestOwnedApplyRefusesForeignResources(t *testing.T) {
	mutations := 0
	k := &kube.Client{Server: "https://example.test", HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" {
			mutations++
		}
		return response(200, kube.Object{"metadata": kube.Object{"labels": kube.Object{"k0nnect.io/connection": "someone-else"}}}), nil
	})}}
	e := applyOwned(context.Background(), k, fixtureConfig(), "/api/v1/configmaps", "existing", kube.Object{"metadata": kube.Object{"name": "existing"}})
	if e == nil || mutations != 0 {
		t.Fatal("foreign resource was mutated")
	}
}
func TestTokenHasBoundedExpiryAndWorkerOnlyUsage(t *testing.T) {
	c := fixtureConfig()
	var created kube.Object
	k := &kube.Client{Server: "https://example.test", CA: []byte("CA"), HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method == "GET" {
			return response(404, kube.Object{}), nil
		}
		if e := json.NewDecoder(r.Body).Decode(&created); e != nil {
			t.Fatal(e)
		}
		return response(201, created), nil
	})}}
	name, encoded, e := issueToken(context.Background(), k, c)
	if e != nil {
		t.Fatal(e)
	}
	if name != tokenName(c) || encoded == "" {
		t.Fatal("invalid token result")
	}
	data := kube.Map(created, "stringData")
	expiry, e := time.Parse(time.RFC3339, kube.Str(data, "expiration"))
	if e != nil {
		t.Fatal(e)
	}
	if time.Until(expiry) > time.Hour || time.Until(expiry) < 59*time.Minute {
		t.Fatal("unbounded token expiry")
	}
	if data["usage-bootstrap-authentication"] != "true" || data["usage-bootstrap-controller-join"] != nil {
		t.Fatal("wrong bootstrap authority")
	}
}
func TestGeneratedScriptsParseAndPreserveData(t *testing.T) {
	c := fixtureConfig()
	c.ClusterID = "test-cluster"
	for name, script := range map[string]string{"prepare": prepareScript(c), "network": networkUnitsScript(), "install": installScript(c, "k0nnect-test-vds"), "stop": stopWorkerScript(c)} {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command("bash", "-n")
			cmd.Stdin = strings.NewReader(script)
			if b, e := cmd.CombinedOutput(); e != nil {
				t.Fatalf("invalid script: %s: %v", b, e)
			}
			if strings.Contains(script, "k0s reset") {
				t.Fatal("installation resets existing user data")
			}
		})
	}
	if !strings.Contains(installScript(c, "profile"), "NetworkNamespacePath=/run/netns/k0nnect") {
		t.Fatal("Worker service is not isolated")
	}
	if !strings.Contains(installScript(c, "profile"), cniPluginsSHA256+"  cni-plugins.tgz") {
		t.Fatal("CNI plugins are not verified against the pinned digest")
	}
}
func TestPrivateGatewaysUseLearnedWireGuardEndpoints(t *testing.T) {
	a := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	b := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))
	n := Network{Port: 31821, Peers: []Peer{{Address: "10.203.74.1", InternalIP: "192.0.2.9", Endpoint: "198.51.100.9:31820", PublicKey: a}, {Address: "10.203.74.2", InternalIP: "192.0.2.14", PublicKey: b}}}
	cfg := string(workerConfiguration(n, a))
	if strings.Count(cfg, "Endpoint =") != 1 || strings.Count(cfg, "AllowedIPs = 0.0.0.0/0") != 1 || !strings.Contains(cfg, "192.0.2.14/32") {
		t.Fatal("invalid star routing or private gateway endpoint")
	}
	if strings.Contains(cfg, "Endpoint = \n") {
		t.Fatal("empty WireGuard endpoint")
	}
}
func TestServingCSRApprovalChecksNodeAndSANs(t *testing.T) {
	for _, bad := range []bool{false, true} {
		t.Run(fmt.Sprint(bad), func(t *testing.T) {
			c := fixtureConfig()
			key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if e != nil {
				t.Fatal(e)
			}
			ip := c.WorkerAddress
			if bad {
				ip = "10.203.74.99"
			}
			der, e := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "system:node:" + c.NodeName, Organization: []string{"system:nodes"}}, DNSNames: []string{c.NodeName}, IPAddresses: []net.IP{net.ParseIP(ip)}}, key)
			if e != nil {
				t.Fatal(e)
			}
			csr := kube.Object{"metadata": kube.Object{"name": "csr-test"}, "spec": kube.Object{"username": "system:node:" + c.NodeName, "signerName": "kubernetes.io/kubelet-serving", "request": base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))}}
			updates := 0
			k := &kube.Client{Server: "https://example.test", HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				if r.Method == "GET" {
					return response(200, kube.Object{"items": []any{csr}}), nil
				}
				updates++
				return response(200, kube.Object{}), nil
			})}}
			e = approveServing(context.Background(), k, c)
			if bad && (e == nil || updates != 0) {
				t.Fatal("approved unexpected SAN")
			}
			if !bad && (e != nil || updates != 1) {
				t.Fatalf("valid node CSR not approved: %v", e)
			}
		})
	}
}

func TestGatewayAffinityUsesOneFieldValuePerTerm(t *testing.T) {
	preview := &previewTransport{read: roundTrip(func(r *http.Request) (*http.Response, error) { return response(404, kube.Object{}), nil }), objects: map[string]kube.Object{}}
	k := &kube.Client{Server: "https://example.test", HTTP: &http.Client{Transport: preview}}
	names := []string{"worker-a", "worker-b", "worker-c"}
	if err := gatewayResources(context.Background(), k, fixtureConfig(), map[string]Network{}, names); err != nil {
		t.Fatal(err)
	}
	for _, r := range preview.requests {
		if kube.Str(r.Object, "kind") != "DaemonSet" {
			continue
		}
		spec := kube.Map(kube.Map(kube.Map(r.Object, "spec"), "template"), "spec")
		affinity := kube.Map(kube.Map(kube.Map(spec, "affinity"), "nodeAffinity"), "requiredDuringSchedulingIgnoredDuringExecution")
		terms := kube.Items(affinity, "nodeSelectorTerms")
		if len(terms) != len(names) {
			t.Fatalf("terms=%d", len(terms))
		}
		for i, term := range terms {
			fields := kube.Items(kube.As(term), "matchFields")
			values := kube.Items(kube.As(fields[0]), "values")
			if len(values) != 1 || values[0] != names[i] {
				t.Fatalf("invalid field values: %v", values)
			}
		}
		return
	}
	t.Fatal("no DaemonSet generated")
}

func TestWorkerProfileGrantIsBoundedToNewIdentityAndConfigMap(t *testing.T) {
	c := fixtureConfig()
	p := &previewTransport{read: roundTrip(func(r *http.Request) (*http.Response, error) { return response(404, kube.Object{}), nil }), objects: map[string]kube.Object{}}
	k := &kube.Client{Server: "https://example.test", HTTP: &http.Client{Transport: p}}
	_, err := workerProfile(context.Background(), k, c, kube.Object{"data": kube.Object{"kubeletConfiguration": "{}"}}, "1.36")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range p.requests {
		if kube.Str(r.Object, "kind") == "Role" {
			rule := kube.As(kube.Items(r.Object, "rules")[0])
			names := kube.Items(rule, "resourceNames")
			if len(names) != 1 || names[0] != "worker-config-k0nnect-test-vds-1.36" {
				t.Fatalf("broad profile grant: %v", names)
			}
			found = true
		}
		if kube.Str(r.Object, "kind") == "RoleBinding" {
			subjects := kube.Items(r.Object, "subjects")
			if len(subjects) != 2 || kube.Str(kube.As(subjects[0]), "name") != "system:node:"+c.NodeName || kube.Str(kube.As(subjects[1]), "name") != "system:bootstrap:"+strings.TrimPrefix(tokenName(c), "bootstrap-token-") {
				t.Fatal("unexpected worker profile subjects")
			}
		}
	}
	if !found {
		t.Fatal("no bounded profile RBAC created")
	}
}

func TestRenameRefusesLegacyInstallationBeforeMutations(t *testing.T) {
	for _, operation := range []string{"connect", "addon"} {
		t.Run(operation, func(t *testing.T) {
			k := &kube.Client{Server: "https://example.test", HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" {
					t.Fatalf("rename made a mutation: %s", r.Method)
				}
				if r.URL.Path != "/api/v1/namespaces" || r.URL.Query().Get("labelSelector") != "app.kubernetes.io/managed-by=k8nnect" {
					t.Fatalf("unexpected preflight: %s", r.URL)
				}
				return response(200, kube.Object{"items": []any{kube.Object{"metadata": kube.Object{"name": "legacy-system"}}}}), nil
			})}}
			var err error
			if operation == "connect" {
				err = Connect(context.Background(), k, fixtureConfig(), io.Discard)
			} else {
				err = InstallAddon(context.Background(), k, fixtureConfig(), io.Discard)
			}
			if err == nil || !strings.Contains(err.Error(), "legacy installation") {
				t.Fatalf("missing migration refusal: %v", err)
			}
		})
	}
}
