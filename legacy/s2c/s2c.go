package s2c

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
	"github.com/m-lab/ndt-cloud/legacy/metrics"
	"github.com/m-lab/ndt-cloud/legacy/protocol"
	"github.com/m-lab/ndt-cloud/legacy/testresponder"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Responder struct {
	testresponder.TestResponder
}

// Result is the result object returned to S2C clients as JSON.
type Result struct {
	ThroughputValue  float64
	UnsentDataAmount int64
	TotalSentByte    int64
}

func (n *Result) String() string {
	b, _ := json.Marshal(n)
	return string(b)
}

// TestServer performs the NDT s2c test.
func (s2c *Responder) TestServer(w http.ResponseWriter, r *http.Request) {
	upgrader := testresponder.MakeNdtUpgrader([]string{"s2c"})
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade should have already returned an HTTP error code.
		log.Println("ERROR S2C: upgrader", err)
		return
	}
	defer ws.Close()
	dataToSend := make([]byte, 81920)
	for i := range dataToSend {
		dataToSend[i] = byte(((i * 101) % (122 - 33)) + 33)
	}
	messageToSend, err := websocket.NewPreparedMessage(websocket.BinaryMessage, dataToSend)
	if err != nil {
		log.Println("ERROR S2C: Could not make prepared message:", err)
		return
	}

	// Signal control channel that we are about to start the test.
	s2c.Response <- testresponder.Ready
	s2c.Response <- s2c.sendUntil(ws, messageToSend, len(dataToSend))
}

func (s2c *Responder) sendUntil(ws *websocket.Conn, msg *websocket.PreparedMessage, dataLen int) float64 {
	// Create ticker to enforce timeout on
	done := make(chan float64)

	go func() {
		totalBytes := float64(0)
		startTime := time.Now()
		endTime := startTime.Add(10 * time.Second)
		for time.Now().Before(endTime) {
			err := ws.WritePreparedMessage(msg)
			if err != nil {
				log.Println("ERROR S2C: sending message", err)
				s2c.Cancel()
				return
			}
			totalBytes += float64(dataLen)
		}
		done <- totalBytes / float64(time.Since(startTime)/time.Second)
	}()

	log.Println("S2C: Waiting for test to complete or timeout")
	select {
	case <-s2c.Ctx.Done():
		log.Println("S2C: Context Done!", s2c.Ctx.Err())
		ws.Close()
		// Return zero on error.
		return 0
	case bytesPerSecond := <-done:
		return bytesPerSecond
	}
}

func ManageTest(ws *websocket.Conn, certFile, keyFile string) (float64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Create a testResponder instance.
	testResponder := &Responder{}

	// Create a TLS server for running the S2C test.
	serveMux := http.NewServeMux()
	serveMux.HandleFunc("/ndt_protocol",
		promhttp.InstrumentHandlerCounter(
			metrics.TestCount.MustCurryWith(prometheus.Labels{"direction": "s2c"}),
			http.HandlerFunc(testResponder.TestServer)))
	err := testResponder.StartTLSAsync(serveMux, "S2C", certFile, keyFile)
	if err != nil {
		return 0, err
	}
	defer testResponder.Close()

	done := make(chan float64)
	go func() {
		// Wait for test to run. ///////////////////////////////////////////
		// Send the server port to the client.
		protocol.SendJSONMessage(protocol.TestPrepare, strconv.Itoa(testResponder.Port), ws)
		s2cReady := <-testResponder.Response
		if s2cReady != testresponder.Ready {
			log.Println("ERROR S2C: Bad value received on the s2c channel", s2cReady)
			cancel()
			return
		}
		protocol.SendJSONMessage(protocol.TestStart, "", ws)
		s2cBytesPerSecond := <-testResponder.Response
		s2cKbps := 8 * s2cBytesPerSecond / 1000.0

		// Send additional download results to the client.
		resultMsg := &Result{
			ThroughputValue:  s2cKbps,
			UnsentDataAmount: 0,
			TotalSentByte:    int64(10 * s2cBytesPerSecond), // TODO: use actual bytes sent.
		}
		err = protocol.WriteMessage(ws, protocol.TestMsg, resultMsg)
		if err != nil {
			log.Println("S2C: Failed to write JSON message:", err)
			cancel()
			return
		}
		clientRateMsg, err := protocol.ReceiveJSONMessage(ws, protocol.TestMsg)
		if err != nil {
			log.Println("S2C: Failed to read JSON message:", err)
			cancel()
			return
		}
		log.Println("S2C: The client sent us:", clientRateMsg.Msg)
		requiredWeb100Vars := []string{"MaxRTT", "MinRTT"}

		for _, web100Var := range requiredWeb100Vars {
			protocol.SendJSONMessage(protocol.TestMsg, web100Var+": 0", ws)
		}
		protocol.SendJSONMessage(protocol.TestFinalize, "", ws)
		clientRate, err := strconv.ParseFloat(clientRateMsg.Msg, 64)
		if err != nil {
			log.Println("S2C: Bad client rate:", err)
			cancel()
			return
		}
		log.Println("S2C: server rate:", s2cKbps, "vs client rate:", clientRate)
		done <- s2cKbps
	}()

	select {
	case <-ctx.Done():
		log.Println("S2C: ctx done!")
		return 0, ctx.Err()
	case rate := <-done:
		log.Println("S2C: finished ", rate)
		return rate, nil
	}
}
