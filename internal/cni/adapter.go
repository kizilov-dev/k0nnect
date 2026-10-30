// Package cni defines explicit compatibility checks. Configuration-file
// validation is not discovery or attestation of a running datapath.
package cni

import (
	"fmt"
	"io"
	"k0nnect/internal/cni/flannel"
	"k0nnect/internal/model"
)

type Adapter interface {
	ValidateProfile(model.Config) error
	ValidateNetworkConfig(model.Config, io.Reader) error
	Requirements(model.Config) []string
}

func ForConfig(c model.Config) (Adapter, error) {
	if c.Kubernetes.CNI.Name != "flannel" {
		return nil, fmt.Errorf("no adapter for CNI %q", c.Kubernetes.CNI.Name)
	}
	a := flannel.Adapter{}
	return a, a.ValidateProfile(c)
}
