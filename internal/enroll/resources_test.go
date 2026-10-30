package enroll

import (
	"context"
	"net/http"
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
