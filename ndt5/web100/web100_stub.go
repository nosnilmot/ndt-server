// +build !linux

package web100

import (
	"context"

	"github.com/m-lab/ndt-server/ndt7/listener"
)

// MeasureViaPolling collects all required data by polling. It is required for
// non-BBR connections because MinRTT is one of our critical metrics.
func MeasureViaPolling(ctx context.Context, mc listener.MagicBBRConn) <-chan *Metrics {
	// Just a stub.
	return nil
}
