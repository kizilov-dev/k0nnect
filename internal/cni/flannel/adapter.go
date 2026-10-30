package flannel

import (
	"encoding/json"
	"fmt"
	"io"
	"k0nnect/internal/model"
)

type Adapter struct{}

func (Adapter) ValidateProfile(c model.Config) error {
	n := c.Kubernetes.CNI
	if n.Name != "flannel" || n.Backend != "vxlan" || n.DirectRouting {
		return fmt.Errorf("Flannel adapter requires VXLAN with DirectRouting=false")
	}
	return nil
}
func (Adapter) Requirements(c model.Config) []string {
	return []string{
		"Configure flanneld --iface=" + c.Transport.Interface + " and VXLAN DirectRouting=false on every node",
		fmt.Sprintf("Verify effective Flannel Pod MTU=%d; recreate existing pods after a reviewed MTU change", c.Transport.MTU-50),
		"Verify Flannel endpoint annotations use transport addresses; config-file validation does not attest the running datapath",
	}
}
func (a Adapter) ValidateNetworkConfig(c model.Config, r io.Reader) error {
	if err := a.ValidateProfile(c); err != nil {
		return err
	}
	var network struct {
		Network    string
		EnableIPv4 *bool
		EnableIPv6 bool
		Backend    struct {
			Type          string
			DirectRouting bool
			Port          int
		}
	}
	d := json.NewDecoder(io.LimitReader(r, 1<<20))
	if err := d.Decode(&network); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("expected exactly one Flannel network config")
	}
	if network.Network != c.Kubernetes.PodCIDR || network.Backend.Type != "vxlan" || network.Backend.DirectRouting || network.EnableIPv6 || (network.EnableIPv4 != nil && !*network.EnableIPv4) || (network.Backend.Port != 0 && network.Backend.Port != 8472) {
		return fmt.Errorf("Flannel file does not match declared IPv4 VXLAN profile (Pod CIDR, backend, direct routing or port)")
	}
	return nil
}
