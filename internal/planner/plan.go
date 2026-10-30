package planner

import (
	"k0nnect/internal/cni"
	"k0nnect/internal/model"
	"k0nnect/internal/transport"
)

type Plan struct {
	Mode         string           `json:"mode"`
	Node         string           `json:"node"`
	Interface    string           `json:"interface"`
	Address      string           `json:"address"`
	MTU          int              `json:"mtu"`
	PodMTU       int              `json:"podMTU"`
	Links        []transport.Link `json:"links"`
	Requirements []string         `json:"requirements"`
}

func Build(c model.Config, name string) (Plan, error) {
	if err := c.Validate(); err != nil {
		return Plan{}, err
	}
	n, err := c.Node(name)
	if err != nil {
		return Plan{}, err
	}
	adapter, err := cni.ForConfig(c)
	if err != nil {
		return Plan{}, err
	}
	p := Plan{Mode: c.Mode, Node: name, Interface: c.Transport.Interface, Address: n.Address + "/32", MTU: c.Transport.MTU, PodMTU: c.Transport.MTU - 50}
	for _, peer := range c.Nodes {
		if peer.Name != name {
			p.Links = append(p.Links, transport.Link{Node: peer.Name, Endpoint: peer.Endpoint, ReachablePrefixes: []string{peer.Address + "/32"}})
		}
	}
	p.Requirements = []string{
		"Experimental profile, not certified against a live Kubernetes cluster",
		"Install the transport on all nodes including control-plane nodes before joining workers",
		"Set kubelet node-ip to the transport address; configure API endpoint/certificate SANs for the inventory apiServer",
		"Allow WireGuard UDP on physical interfaces and CNI/kubelet/API traffic on the encrypted interface",
		"pathMTU is an operator-supplied lower bound, not a measured path MTU",
		"No automatic firewall, CNI, Kubernetes, or default-route changes",
	}
	p.Requirements = append(p.Requirements, adapter.Requirements(c)...)
	return p, nil
}
