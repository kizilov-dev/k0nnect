// Package transport defines the driver boundary. Transport-specific keys and
// device configuration are deliberately absent from the Link contract.
package transport

import "context"

type Capabilities struct {
	IPv4      bool `json:"ipv4"`
	IPv6      bool `json:"ipv6"`
	Encrypted bool `json:"encrypted"`
	Relay     bool `json:"relay"`
}
type Link struct {
	Node              string   `json:"node"`
	Endpoint          string   `json:"endpoint"`
	ReachablePrefixes []string `json:"reachablePrefixes"`
}
type Status struct {
	Node          string `json:"node"`
	LastHandshake int64  `json:"lastHandshake"`
	Received      uint64 `json:"received"`
	Sent          uint64 `json:"sent"`
}
type Driver interface {
	Capabilities() Capabilities
	Reconcile(context.Context) error
	Status(context.Context) ([]Status, error)
	Remove(context.Context) error
}
