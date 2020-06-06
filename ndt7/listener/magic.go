package listener

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"time"

	guuid "github.com/google/uuid"
	"github.com/m-lab/ndt-server/bbr"
	"github.com/m-lab/ndt-server/fdcache"
	"github.com/m-lab/ndt-server/tcpinfox"
	"github.com/m-lab/tcp-info/inetdiag"
	"github.com/m-lab/tcp-info/tcp"
	"github.com/m-lab/uuid"
)

type MagicListener struct {
	*net.TCPListener
}

type MagicConn struct {
	net.Conn
	File *os.File
}

type MagicAddr struct {
	net.Addr
	mc *MagicConn
}

type MagicBBRConn interface {
	GetUUID() (string, error)
	EnableBBR() error
	ReadInfo() (inetdiag.BBRInfo, tcp.LinuxTCPInfo, error)
}

func (ma *MagicAddr) GetConn() MagicBBRConn {
	return ma.mc
}

func (mc *MagicConn) LocalAddr() net.Addr {
	return &MagicAddr{
		Addr: mc.Conn.LocalAddr(),
		mc:   mc,
	}
}

// Close the underlying TCPConn and duplicate file pointer.
func (mc *MagicConn) Close() error {
	mc.File.Close()
	return mc.Conn.Close()
}

// EnableBBR sets the BBR congestion control on the TCP connection if supported
// by the kernel. If unsupported, EnableBBR has no effect.
func (mc *MagicConn) EnableBBR() error {
	return bbr.Enable(mc.File)
}

// ReadInfo reads metadata about the TCP connections. If BBR was
// not enabled on the underlying connection, then ReadInfo will
// return an error.
func (mc *MagicConn) ReadInfo() (inetdiag.BBRInfo, tcp.LinuxTCPInfo, error) {
	bbrinfo, err := bbr.GetMaxBandwidthAndMinRTT(mc.File)
	if err != nil {
		bbrinfo = inetdiag.BBRInfo{}
	}
	tcpInfo, err := tcpinfox.GetTCPInfo(mc.File)
	if err != nil {
		return inetdiag.BBRInfo{}, tcp.LinuxTCPInfo{}, err
	}
	return bbrinfo, *tcpInfo, nil
}

// GetUUID returns the connection's UUID.
func (mc *MagicConn) GetUUID() (string, error) {
	id, err := uuid.FromFile(mc.File)
	if err != nil {
		// Use UUID v1 as fallback when SO_COOKIE isn't supported by kernel
		fallbackUUID, err := guuid.NewUUID()
		if err != nil {
			return "", fmt.Errorf("unable to fallback to uuid: %s", err.Error())
		}

		id = fallbackUUID.String()
		if id == "" {
			return "", errors.New("unable to fallback to uuid: invalid uuid")
		}
	}
	return id, nil
}

func (ln *MagicListener) Accept() (net.Conn, error) {
	tc, err := ln.AcceptTCP()
	if err != nil {
		return nil, err
	}
	tc.SetKeepAlive(true)
	tc.SetKeepAlivePeriod(3 * time.Minute)
	fp, err := fdcache.TCPConnToFile(tc)
	if err != nil {
		log.Println("Error: could not read *os.File for connection from:", tc.RemoteAddr())
		tc.Close()
		return nil, err
	}
	mc := &MagicConn{
		Conn: tc,
		File: fp,
	}
	return mc, nil
}
