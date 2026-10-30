package kubeadm

import (
	"encoding/json"
	"k8nnect/internal/model"
	"strings"
	"testing"
)

func TestJoinPinsCAAndNodeIPWithoutControlPlaneAccess(t *testing.T) {
	c, e := model.Load("../../../examples/inventory.json")
	if e != nil {
		t.Fatal(e)
	}
	b, e := Render(c, "remote-worker", "abcdef.0123456789abcdef", "sha256:"+strings.Repeat("a", 64))
	if e != nil {
		t.Fatal(e)
	}
	var v map[string]any
	if e = json.Unmarshal(b, &v); e != nil {
		t.Fatal(e)
	}
	if _, ok := v["controlPlane"]; ok {
		t.Fatal("worker join grants control-plane membership")
	}
	if !strings.Contains(string(b), `"value": "10.203.0.2"`) || strings.Contains(string(b), "unsafeSkipCAVerification") || !strings.Contains(string(b), "caCertHashes") {
		t.Fatal("missing pinned identity")
	}
	if _, e = Render(c, "remote-worker", "bad", "sha256:"+strings.Repeat("a", 64)); e == nil {
		t.Fatal("invalid token accepted")
	}
}
