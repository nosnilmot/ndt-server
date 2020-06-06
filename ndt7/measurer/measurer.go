// Package measurer collects metrics from a socket connection
// and returns them for consumption.
package measurer

import (
	"context"
	"time"

	"github.com/m-lab/ndt-server/ndt7/listener"

	"github.com/gorilla/websocket"
	"github.com/m-lab/go/memoryless"
	"github.com/m-lab/ndt-server/logging"
	"github.com/m-lab/ndt-server/ndt7/model"
	"github.com/m-lab/ndt-server/ndt7/spec"
)

// Measurer performs measurements
type Measurer struct {
	conn *websocket.Conn
	uuid string
}

// New creates a new measurer instance
func New(conn *websocket.Conn, UUID string) *Measurer {
	return &Measurer{
		conn: conn,
		uuid: UUID,
	}
}

func (m *Measurer) getSocketAndPossiblyEnableBBR() (listener.MagicBBRConn, error) {
	mc := m.conn.UnderlyingConn().LocalAddr().(*listener.MagicAddr).GetConn()
	err := mc.EnableBBR()
	if err != nil {
		logging.Logger.WithError(err).Warn("Cannot enable BBR")
		// FALLTHROUGH
	}
	return mc, nil
}

func measure(measurement *model.Measurement, mc listener.MagicBBRConn, elapsed time.Duration) {
	// Implementation note: we always want to sample BBR before TCPInfo so we
	// will know from TCPInfo if the connection has been closed.
	t := int64(elapsed / time.Microsecond)
	bbrinfo, tcpInfo, err := mc.ReadInfo()
	if err == nil {
		measurement.BBRInfo = &model.BBRInfo{
			BBRInfo:     bbrinfo,
			ElapsedTime: t,
		}
		measurement.TCPInfo = &model.TCPInfo{
			LinuxTCPInfo: tcpInfo,
			ElapsedTime:  t,
		}
	}
}

func (m *Measurer) loop(ctx context.Context, dst chan<- model.Measurement) {
	logging.Logger.Debug("measurer: start")
	defer logging.Logger.Debug("measurer: stop")
	defer close(dst)
	measurerctx, cancel := context.WithTimeout(ctx, spec.DefaultRuntime)
	defer cancel()
	mc, err := m.getSocketAndPossiblyEnableBBR()
	if err != nil {
		logging.Logger.WithError(err).Warn("getSocketAndPossiblyEnableBBR failed")
		return
	}
	start := time.Now()
	connectionInfo := &model.ConnectionInfo{
		Client: m.conn.RemoteAddr().String(),
		Server: m.conn.LocalAddr().String(),
		UUID:   m.uuid,
	}
	// Implementation note: the ticker will close its output channel
	// after the controlling context is expired.
	ticker, err := memoryless.NewTicker(measurerctx, memoryless.Config{
		Min:      spec.MinPoissonSamplingInterval,
		Expected: spec.AveragePoissonSamplingInterval,
		Max:      spec.MaxPoissonSamplingInterval,
	})
	if err != nil {
		logging.Logger.WithError(err).Warn("memoryless.NewTicker failed")
		return
	}
	defer ticker.Stop()
	for {
		now, active := <-ticker.C
		if !active {
			return
		}
		var measurement model.Measurement
		measure(&measurement, mc, now.Sub(start))
		measurement.ConnectionInfo = connectionInfo
		dst <- measurement // Liveness: this is blocking
	}
}

// Start runs the measurement loop in a background goroutine and emits
// the measurements on the returned channel.
//
// Liveness guarantee: the measurer will always terminate after
// a timeout of DefaultRuntime seconds, provided that the consumer
// continues reading from the returned channel.
func (m *Measurer) Start(ctx context.Context) <-chan model.Measurement {
	dst := make(chan model.Measurement)
	go m.loop(ctx, dst)
	return dst
}
