// Package health serves liveness and readiness, and probes readiness for the
// container HEALTHCHECK.
package health

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/http/router"
	"github.com/osbits/gorgany/v2/service/dto"
	"gorm.io/gorm"

	"myapp/pkg/buildinfo"
)

var unavailable = core.HttpStatus{Status: http.StatusServiceUnavailable, Code: "UNAVAILABLE"}

type Controller struct {
	DBContext core.IDBContext `container:"inject"`
}

func NewController() *Controller { return &Controller{} }

func (c *Controller) GetRoutes() []core.IRouteConfig {
	return []core.IRouteConfig{
		&router.RouteConfig{Path: "/healthz", Method: core.GET, Handler: c.Live, Name: "health.live"},
		&router.RouteConfig{Path: "/readyz", Method: core.GET, Handler: c.Ready, Name: "health.ready"},
	}
}

// Live answers while the process serves HTTP. It touches no dependency, so an
// orchestrator never restarts the app because the database blinked.
func (c *Controller) Live(message core.HttpMessage) {
	body := map[string]string{"version": buildinfo.Version}
	message.Response().JSON(dto.ReturnObject(body, core.SuccessHttpStatus, nil), http.StatusOK)
}

// Ready answers 200 only while the default datasource answers a ping. Deploys and
// load balancers gate on it.
func (c *Controller) Ready(message core.HttpMessage) {
	if err := ping(message.Context(), c.DBContext); err != nil {
		message.Response().JSON(dto.ReturnObject(nil, unavailable, "database unavailable"), http.StatusServiceUnavailable)
		return
	}
	message.Response().JSON(dto.ReturnObject(nil, core.SuccessHttpStatus, nil), http.StatusOK)
}

func ping(ctx context.Context, databases core.IDBContext) error {
	source := databases.GetDataSource(core.DefaultKeyInRegistrar)
	if source == nil {
		return errors.New("no default datasource")
	}
	raw, err := source.GetDriver()
	if err != nil {
		return err
	}
	gormDB, ok := raw.(*gorm.DB)
	if !ok {
		return fmt.Errorf("driver is %T, not *gorm.DB", raw)
	}
	sqlDB, err := gormDB.DB()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return sqlDB.PingContext(ctx)
}
