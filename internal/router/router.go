package router

import (
	"context"
	"errors"

	"github.com/goodnightzsj/router-device-proxy/internal/device"
	"github.com/goodnightzsj/router-device-proxy/internal/policy"
)

var (
	ErrUnsupported = errors.New("operation not supported by this router")
	ErrAuth        = errors.New("router authentication failed")
	ErrUnavailable = errors.New("router unavailable")
)

// DeviceSource returns the router's current device table.
type DeviceSource interface {
	Devices(ctx context.Context) ([]device.Device, error)
}

// NameSource returns MAC -> display name. It must not write to the router.
type NameSource interface {
	Names(ctx context.Context) (map[string]string, error)
}

// PolicyApplier renders and applies the user's proxy policy.
type PolicyApplier interface {
	Apply(ctx context.Context, policies map[string]policy.Policy) (string, error)
}

// PersistenceInstaller installs a boot-time restore hook.
type PersistenceInstaller interface {
	Install(ctx context.Context) (string, error)
}

// Health reports whether the router is reachable and runs a credential test.
type Health interface {
	OK(ctx context.Context) bool
	Test(ctx context.Context) (string, error)
}

type Capabilities struct {
	Devices     bool `json:"devices"`
	Names       bool `json:"names"`
	Policy      bool `json:"policy"`
	Persistence bool `json:"persistence"`
}

type Set struct {
	Devices     DeviceSource
	Names       []NameSource
	Policy      PolicyApplier
	Persistence PersistenceInstaller
	Health      Health
	Brand       string
	Caps        Capabilities
}

func (s Set) Capabilities() Capabilities {
	return s.Caps
}
