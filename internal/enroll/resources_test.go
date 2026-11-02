package enroll

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
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

// Outside its own namespace the installer may create objects, but every read
// that is not cluster discovery and every update or delete names its objects.
func TestInstallerRBACIsScopedByName(t *testing.T) {
	c := fixtureConfig()
	c.PasswordFile = filepath.Join(t.TempDir(), "password")
	if e := os.WriteFile(c.PasswordFile, []byte("not-a-real-password"), 0600); e != nil {
		t.Fatal(e)
	}
	read := roundTrip(func(r *http.Request) (*http.Response, error) { return response(404, kube.Object{}), nil })
	preview := &previewTransport{read: read, objects: map[string]kube.Object{}}
	k := &kube.Client{Server: "https://example.test", HTTP: &http.Client{Transport: preview}}
	if e := InstallAddon(context.Background(), k, c, io.Discard); e != nil {
		t.Fatal(e)
	}
	discovery := map[string]bool{"nodes": true, "namespaces": true, "services": true, "daemonsets": true, "ippools": true, "certificatesigningrequests": true}
	checked := 0
	for _, r := range preview.requests {
		kind, ns := kube.Str(r.Object, "kind"), kube.Str(kube.Map(r.Object, "metadata"), "namespace")
		if kind != "ClusterRole" && !(kind == "Role" && ns == "kube-system") {
			continue
		}
		if !strings.HasSuffix(kube.Str(kube.Map(r.Object, "metadata"), "name"), "-installer") && !strings.HasSuffix(kube.Str(kube.Map(r.Object, "metadata"), "name"), "-installer-local") {
			continue
		}
		checked++
		for _, v := range kube.Items(r.Object, "rules") {
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
		t.Fatalf("checked %d installer roles, want ClusterRole and kube-system Role", checked)
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
