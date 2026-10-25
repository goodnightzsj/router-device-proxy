package generic

import (
	"context"

	"github.com/goodnightzsj/router-device-proxy/internal/policy"
	"github.com/goodnightzsj/router-device-proxy/internal/router"
	"github.com/goodnightzsj/router-device-proxy/internal/router/openwrt"
)

type Adapter struct {
	*openwrt.Adapter
}

func New(a *openwrt.Adapter) *Adapter { return &Adapter{Adapter: a} }

func (a *Adapter) Apply(ctx context.Context, policies map[string]policy.Policy) (string, error) {
	return "", router.ErrUnsupported
}

func (a *Adapter) Install(ctx context.Context) (string, error) {
	return "", router.ErrUnsupported
}

func (a *Adapter) Test(ctx context.Context) (string, error) {
	return a.Adapter.Test(ctx)
}

var _ router.DeviceSource = (*Adapter)(nil)
var _ router.PolicyApplier = (*Adapter)(nil)
var _ router.PersistenceInstaller = (*Adapter)(nil)
var _ router.Health = (*Adapter)(nil)
