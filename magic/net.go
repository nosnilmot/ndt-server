package magic

import (
	"crypto/tls"
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

type Listener struct {
	*net.TCPListener
}

type Conn struct {
	net.Conn
	File *os.File
}

type Addr struct {
	net.Addr
	mc *Conn
}

type ConnInfo interface {
	GetUUID() (string, error)
	EnableBBR() error
	ReadInfo() (inetdiag.BBRInfo, tcp.LinuxTCPInfo, error)
}

// Accept a connection, set its keepalive time, and return a Conn that enables
// actions on the underlying net.Conn file descriptor.
func (ln *Listener) Accept() (net.Conn, error) {
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
	mc := &Conn{
		Conn: tc,
		File: fp,
	}
	return mc, nil
}

// Close the underlying TCPConn and duplicate file pointer.
func (mc *Conn) Close() error {
	mc.File.Close()
	return mc.Conn.Close()
}

// EnableBBR sets the BBR congestion control on the TCP connection if supported
// by the kernel. If unsupported, EnableBBR has no effect.
func (mc *Conn) EnableBBR() error {
	return bbr.Enable(mc.File)
}

// ReadInfo reads metadata about the TCP connections. If BBR was
// not enabled on the underlying connection, then ReadInfo will
// return an error.
func (mc *Conn) ReadInfo() (inetdiag.BBRInfo, tcp.LinuxTCPInfo, error) {
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
func (mc *Conn) GetUUID() (string, error) {
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

func (mc *Conn) LocalAddr() net.Addr {
	return &Addr{
		Addr: mc.Conn.LocalAddr(),
		mc:   mc,
	}
}

func ToTCPAddr(addr net.Addr) *net.TCPAddr {
	switch a := addr.(type) {
	case *Addr:
		return a.Addr.(*net.TCPAddr)
	case *net.TCPAddr:
		return a
	default:
		log.Fatalf("unsupported conn type: %T", a)
		return nil
	}
}

func ToConnInfo(conn net.Conn) ConnInfo {
	switch c := conn.(type) {
	case *Conn:
		return c
	case *tls.Conn:
		return c.LocalAddr().(*Addr).GetConn()
	default:
		panic(fmt.Sprintf("unsupported conn type: %T", c))
	}
}

func (ma *Addr) GetConn() ConnInfo {
	return ma.mc
}
