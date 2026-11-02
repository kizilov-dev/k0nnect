package enroll

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func TestValidateNetworkNodeMTU(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	base := Network{Name: "test-vds", NodeName: "external-node", CIDR: "10.203.74.0/24", Address: "10.203.74.100", WorkerAddress: "10.203.74.100", WorkerEndpoint: "198.51.100.2:31821", WorkerKey: key, Port: 31821, MTU: 1400}
	for _, tc := range []struct {
		nodeMTU int
		err     string
	}{{0, ""}, {1400, ""}, {1500, ""}, {9000, ""}, {1399, "node MTU"}, {9001, "node MTU"}} {
		n := base
		n.NodeMTU = tc.nodeMTU
		e := validateNetwork(n)
		if tc.err == "" && e != nil {
			t.Fatalf("node MTU %d rejected: %v", tc.nodeMTU, e)
		}
		if tc.err != "" && (e == nil || !strings.Contains(e.Error(), tc.err)) {
			t.Fatalf("node MTU %d: error %v, want %q", tc.nodeMTU, e, tc.err)
		}
	}
}

func TestGatewayRulesBlockLinkLocal(t *testing.T) {
	n := Network{Name: "test-vds", WorkerAddress: "10.203.74.100"}
	var accept, drop bool
	for _, r := range gatewayRules(n) {
		rule := strings.Join(r, " ")
		if strings.Contains(rule, "FORWARD -i "+GatewayInterface) {
			accept = accept || strings.Contains(rule, "! -d "+LinkLocal) && strings.HasSuffix(rule, "-j ACCEPT")
			drop = drop || strings.Contains(rule, " -d "+LinkLocal) && !strings.Contains(rule, "!") && strings.HasSuffix(rule, "-j DROP")
			if strings.HasSuffix(rule, "-j ACCEPT") && !strings.Contains(rule, "! -d "+LinkLocal) {
				t.Fatalf("Worker traffic accepted towards link-local: %s", rule)
			}
		}
	}
	if !accept || !drop {
		t.Fatalf("forwarding rules do not exclude and drop link-local: accept=%v drop=%v", accept, drop)
	}
	if strings.Contains(strings.Join(legacyGatewayRules(n)[0], " "), LinkLocal) {
		t.Fatal("legacy rule must be the unrestricted accept it replaces")
	}
}
