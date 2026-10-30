package flannel

import (
	"k0nnect/internal/model"
	"strings"
	"testing"
)

func TestConfigurationCompatibility(t *testing.T) {
	c, e := model.Load("../../../examples/inventory.json")
	if e != nil {
		t.Fatal(e)
	}
	valid := `{"Network":"10.244.0.0/16","Backend":{"Type":"vxlan","DirectRouting":false}}`
	if e = (Adapter{}).ValidateNetworkConfig(c, strings.NewReader(valid)); e != nil {
		t.Fatal(e)
	}
	for _, bad := range []string{
		strings.Replace(valid, "vxlan", "host-gw", 1),
		strings.Replace(valid, "false", "true", 1),
		strings.Replace(valid, "10.244.0.0/16", "10.245.0.0/16", 1),
		strings.Replace(valid, `"Network"`, `"EnableIPv6":true,"Network"`, 1),
	} {
		if e = (Adapter{}).ValidateNetworkConfig(c, strings.NewReader(bad)); e == nil {
			t.Fatalf("accepted incompatible Flannel config %s", bad)
		}
	}
}
