// Package receiver implements the messages receiver. It can be used
// both by the download and the upload subtests.
package receiver

import (
	"context"
	"encoding/json"
	"time"

	"github.com/gorilla/websocket"
	"github.com/m-lab/ndt-server/logging"
	ndt7metrics "github.com/m-lab/ndt-server/ndt7/metrics"
	"github.com/m-lab/ndt-server/ndt7/model"
	"github.com/m-lab/ndt-server/ndt7/ping/message"
	"github.com/m-lab/ndt-server/ndt7/spec"
)

type receiverKind int

const (
	downloadReceiver = receiverKind(iota)
	uploadReceiver
)

func start(
	ctx context.Context, conn *websocket.Conn, kind receiverKind,
	data *model.ArchivalData,
) {
	logging.Logger.Debug("receiver: start")
	proto := ndt7metrics.ConnLabel(conn)
	defer logging.Logger.Debug("receiver: stop")
	conn.SetReadLimit(spec.MaxMessageSize)
	receiverctx, cancel := context.WithTimeout(ctx, spec.MaxRuntime)
	defer cancel()
	err := conn.SetReadDeadline(data.StartTime.Add(spec.MaxRuntime)) // Liveness!
	if err != nil {
		logging.Logger.WithError(err).Warn("receiver: conn.SetReadDeadline failed")
		ndt7metrics.ClientReceiverErrors.WithLabelValues(
			proto, string(kind), "set-read-deadline").Inc()
		return
	}
	conn.SetPongHandler(func(s string) error {
		_, rtt, err := message.ParseTicks(s, data.StartTime)
		if err == nil {
			logging.Logger.Debugf("receiver: ApplicationLevel RTT: %d ms", int64(rtt / time.Millisecond))
		} else {
			ndt7metrics.ClientReceiverErrors.WithLabelValues(
				proto, string(kind), "ping-parse-ticks").Inc()
		}
		return err
	})
	for receiverctx.Err() == nil { // Liveness!
		mtype, mdata, err := conn.ReadMessage()
		if err != nil {
			ndt7metrics.ClientReceiverErrors.WithLabelValues(
				proto, string(kind), "read-message").Inc()
			return
		}
		if mtype != websocket.TextMessage {
			switch kind {
			case downloadReceiver:
				logging.Logger.Warn("receiver: got non-Text message")
				ndt7metrics.ClientReceiverErrors.WithLabelValues(
					proto, string(kind), "wrong-message-type").Inc()
				return // Unexpected message type
			default:
				// NOTE: this is the bulk upload path. In this case, the mdata is not used.
				continue // No further processing required
			}
		}
		var measurement model.Measurement
		err = json.Unmarshal(mdata, &measurement)
		if err != nil {
			logging.Logger.WithError(err).Warn("receiver: json.Unmarshal failed")
			ndt7metrics.ClientReceiverErrors.WithLabelValues(
				proto, string(kind), "unmarshal-client-message").Inc()
			return
		}
		data.ClientMeasurements = append(data.ClientMeasurements, measurement)
	}
	ndt7metrics.ClientReceiverErrors.WithLabelValues(
		proto, string(kind), "receiver-context-expired").Inc()
}

// StartDownloadReceiverAsync starts the receiver in a background goroutine and
// saves messages received from the client in the given archival data. The
// returned context may be used to detect when the receiver has completed.
//
// This receiver will not tolerate receiving binary messages. It will terminate
// early if such a message is received.
//
// Liveness guarantee: the goroutine will always terminate after a MaxRuntime
// timeout.
func StartDownloadReceiverAsync(ctx context.Context, conn *websocket.Conn, data *model.ArchivalData) context.Context {
	ctx2, cancel2 := context.WithCancel(ctx)
	go func() {
		start(ctx2, conn, downloadReceiver, data)
		cancel2()
	}()
	return ctx2
}

// StartUploadReceiverAsync is like StartDownloadReceiverAsync except that it
// tolerates incoming binary messages, sent by "upload" measurement clients to
// create network load, and therefore must be allowed.
func StartUploadReceiverAsync(ctx context.Context, conn *websocket.Conn, data *model.ArchivalData) context.Context {
	ctx2, cancel2 := context.WithCancel(ctx)
	go func() {
		start(ctx2, conn, uploadReceiver, data)
		cancel2()
	}()
	return ctx2
}
