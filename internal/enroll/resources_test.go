package enroll

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"k0nnect/internal/kube"
)

func TestEnsureNamespaceWaitsForTermination(t *testing.T) {
	c := fixtureConfig()
	gets, created := 0, false
	rt := roundTrip(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == "GET":
			gets++
			if gets == 1 {
				// Left over by a just-finished disconnect.
				return response(200, kube.Object{"metadata": kube.Object{"name": c.Namespace, "deletionTimestamp": "2026-10-30T12:00:00Z", "annotations": kube.Object{"k0nnect.io/connection": c.Name}}}), nil
			}
			return response(404, kube.Object{}), nil
		case r.Method == "POST" && r.URL.Path == "/api/v1/namespaces":
			created = true
			return response(201, kube.Object{}), nil
		}
		t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		return nil, nil
	})
	k := &kube.Client{Server: "https://example.test", HTTP: &http.Client{Transport: rt}}
	if e := ensureNamespace(context.Background(), k, c); e != nil {
		t.Fatal(e)
	}
	if !created || gets != 2 {
		t.Fatalf("namespace recreated=%v after %d reads", created, gets)
	}
}

// Outside its own namespace the controller may create objects, but every read
// that is not cluster discovery and every update or delete names its objects.
func TestControllerRBACIsScopedByName(t *testing.T) {
	objects := controllerManifest(t)
	discovery := map[string]bool{"nodes": true, "namespaces": true, "services": true, "ippools": true, "certificatesigningrequests": true, "pods": true}
	checked := 0
	for _, obj := range objects {
		kind, ns := kube.Str(obj, "kind"), kube.Str(kube.Map(obj, "metadata"), "namespace")
		if kind != "ClusterRole" && !(kind == "Role" && ns == "kube-system") {
			continue
		}
		checked++
		for _, v := range kube.Items(obj, "rules") {
			rule := kube.As(v)
			if len(kube.Items(rule, "resourceNames")) > 0 {
				continue
			}
			for _, verb := range kube.Items(rule, "verbs") {
				for _, res := range kube.Items(rule, "resources") {
					switch {
					case verb == "create":
					case (verb == "get" || verb == "list") && discovery[fmt.Sprint(res)]:
					case verb == "update" && res == "certificatesigningrequests/approval":
					default:
						t.Errorf("%s %s grants %s on all %s", kind, ns, verb, res)
					}
				}
			}
		}
	}
	if checked != 2 {
		t.Fatalf("checked %d controller roles, want ClusterRole and kube-system Role", checked)
	}
	if rbacAllows(objects, "patch", "", "nodes", "", "worker-a") || !rbacAllows(objects, "patch", "", "nodes", "", ControllerNode) {
		t.Fatal("node writes are not scoped to the external node")
	}
	if !rbacAllows(objects, "delete", "", "secrets", "kube-system", tokenName(controllerConfig(""))) || rbacAllows(objects, "get", "", "secrets", "kube-system", "other") {
		t.Fatal("bootstrap token permissions are not scoped to the controller's token")
	}
	for _, obj := range objects {
		if kube.Str(obj, "kind") == "Deployment" {
			spec := kube.Map(kube.Map(kube.Map(obj, "spec"), "template"), "spec")
			security := kube.Map(kube.As(kube.Items(spec, "containers")[0]), "securityContext")
			if spec["hostNetwork"] == true || security["runAsNonRoot"] != true || security["readOnlyRootFilesystem"] != true {
				t.Fatal("controller Pod is not an unprivileged, non-root workload")
			}
		}
	}
}

const testImage = "ghcr.io/example/k0nnect@sha256:0000000000000000000000000000000000000000000000000000000000000000"

// controllerManifest returns the install manifest as the API server reads it.
func controllerManifest(t *testing.T) []kube.Object {
	objects, e := ControllerManifest(testImage)
	if e != nil {
		t.Fatal(e)
	}
	b, e := json.Marshal(objects)
	if e != nil {
		t.Fatal(e)
	}
	var decoded []kube.Object
	if e = json.Unmarshal(b, &decoded); e != nil {
		t.Fatal(e)
	}
	return decoded
}

// rbacRequest maps a Kubernetes API request to the RBAC attributes it needs.
func rbacRequest(r *http.Request) (verb, group, resource, namespace, name string) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if parts[0] == "api" {
		parts = parts[2:]
	} else {
		group, parts = parts[1], parts[3:]
	}
	if len(parts) >= 3 && parts[0] == "namespaces" {
		namespace, parts = parts[1], parts[2:]
	}
	resource = parts[0]
	if len(parts) > 1 {
		name = parts[1]
	}
	if len(parts) > 2 {
		resource += "/" + parts[2]
	}
	verb = map[string]string{"POST": "create", "PATCH": "patch", "PUT": "update", "DELETE": "delete"}[r.Method]
	if r.Method == "GET" {
		verb = "get"
		if name == "" {
			verb = "list"
		}
	}
	return
}

func rbacAllows(objects []kube.Object, verb, group, resource, namespace, name string) bool {
	for _, obj := range objects {
		kind, ns := kube.Str(obj, "kind"), kube.Str(kube.Map(obj, "metadata"), "namespace")
		if kind != "ClusterRole" && !(kind == "Role" && ns == namespace) {
			continue
		}
		for _, v := range kube.Items(obj, "rules") {
			rule := kube.As(v)
			in := func(key, value string) bool {
				return strings.Contains(" "+strings.Trim(fmt.Sprint(kube.Items(rule, key)), "[]")+" ", " "+value+" ")
			}
			names := kube.Items(rule, "resourceNames")
			if in("apiGroups", group) && in("resources", resource) && in("verbs", verb) && (len(names) == 0 || name != "" && verb != "create" && verb != "list" && in("resourceNames", name)) {
				return true
			}
		}
	}
	return false
}

// The controller's own requests during connect and disconnect stay within the
// RBAC of the install manifest.
func TestControllerRBACCoversItsRequests(t *testing.T) {
	image, objects := testImage, controllerManifest(t)
	c := controllerConfig(image)
	c.Host, c.HostKeySHA256, c.TransportCIDR, c.WorkerAddress = "198.51.100.2", "SHA256:012345678901234567890123456789012345678901234", "10.203.74.0/24", "10.203.74.100"
	if e := c.Validate(); e != nil {
		t.Fatal(e)
	}
	rt := roundTrip(func(r *http.Request) (*http.Response, error) {
		verb, group, resource, namespace, name := rbacRequest(r)
		if !rbacAllows(objects, verb, group, resource, namespace, name) {
			t.Errorf("controller RBAC forbids %s %s/%s %s/%s", verb, group, resource, namespace, name)
		}
		switch {
		case r.Method != "GET":
			return response(200, kube.Object{}), nil
		case r.URL.Path == "/api/v1/namespaces/"+c.Namespace:
			return response(200, kube.Object{"metadata": kube.Object{"name": c.Namespace, "annotations": kube.Object{"k0nnect.io/controller": c.Name}}}), nil
		case r.URL.Path == connectionPath:
			return response(200, kube.Object{"metadata": kube.Object{"name": c.Name, "resourceVersion": "1"}}), nil
		case r.URL.Path == "/api/v1/namespaces/"+c.Namespace+"/secrets/"+SSHSecret:
			return response(200, kube.Object{"data": kube.Object{"password": "c2VjcmV0"}}), nil
		case name == "":
			return response(200, kube.Object{"items": []any{}}), nil
		}
		return response(404, kube.Object{}), nil
	})
	k := &kube.Client{Server: "https://example.test", HTTP: &http.Client{Transport: rt}}
	ctx := context.Background()
	cr := kube.Object{"metadata": kube.Object{"name": c.Name, "generation": 1.0}, "spec": kube.Object{"host": c.Host, "hostKeySHA256": c.HostKeySHA256, "transportCIDR": c.TransportCIDR, "workerAddress": c.WorkerAddress}}
	if _, e := connectionConfig(ctx, k, cr, image); e != nil {
		t.Fatal(e)
	}
	if e := editFinalizers(ctx, k, func([]string) []string { return []string{finalizer} }); e != nil {
		t.Fatal(e)
	}
	setStatus(ctx, k, cr, "Ready", "", io.Discard)
	if e := ensureNamespace(ctx, k, c); e != nil {
		t.Fatal(e)
	}
	if e := gatewayResources(ctx, k, c, map[string]Network{}, []string{"worker-a"}); e != nil {
		t.Fatal(e)
	}
	source := kube.Object{"data": kube.Object{"kubeletConfiguration": "{}"}}
	if _, e := workerProfile(ctx, k, c, source, "1.36"); e != nil {
		t.Fatal(e)
	}
	if _, _, e := issueToken(ctx, k, c); e != nil {
		t.Fatal(e)
	}
	if e := approveServing(ctx, k, c); e != nil {
		t.Fatal(e)
	}
	// Without SSH credentials and with nothing started on the VDS, disconnect
	// cleans the cluster objects only.
	if e := Disconnect(ctx, k, c, true, io.Discard); e != nil {
		t.Fatal(e)
	}
}

// Gateways publish into their own ConfigMap: neither they nor the installer,
// which grants their role and is bound by RBAC escalation checks, patch Nodes.
func TestGatewayRBACAvoidsNodePatches(t *testing.T) {
	c := fixtureConfig()
	read := roundTrip(func(r *http.Request) (*http.Response, error) { return response(404, kube.Object{}), nil })
	preview := &previewTransport{read: read, objects: map[string]kube.Object{}}
	k := &kube.Client{Server: "https://example.test", HTTP: &http.Client{Transport: preview}}
	if e := gatewayResources(context.Background(), k, c, map[string]Network{}, []string{"worker-a", "worker-b"}); e != nil {
		t.Fatal(e)
	}
	var stateCreated, statePatchable bool
	for _, r := range preview.requests {
		meta := kube.Map(r.Object, "metadata")
		switch kube.Str(r.Object, "kind") {
		case "ClusterRole":
			for _, v := range kube.Items(r.Object, "rules") {
				for _, verb := range kube.Items(kube.As(v), "verbs") {
					if verb != "get" {
						t.Fatalf("gateway ClusterRole grants %v", verb)
					}
				}
			}
		case "Role":
			for _, v := range kube.Items(r.Object, "rules") {
				rule := kube.As(v)
				if fmt.Sprint(kube.Items(rule, "resourceNames")) == "["+GatewayState+"]" && strings.Contains(fmt.Sprint(kube.Items(rule, "verbs")), "patch") {
					statePatchable = true
				}
			}
		case "ConfigMap":
			if kube.Str(meta, "name") == GatewayState {
				stateCreated = true
				if _, ok := r.Object["data"]; ok {
					t.Fatal("reconcile would overwrite published gateway state")
				}
			}
		}
	}
	if !stateCreated || !statePatchable {
		t.Fatalf("gateway state ConfigMap created=%v patchable=%v", stateCreated, statePatchable)
	}
}
