// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

// Package transport sends bytes to a printer: network (raw TCP 9100),
// serial (USB-serial / COM port), a fake, plus the Windows-only "winspool"
// transport (RAW bytes to an installed Windows printer by name).
//
// Reliability rules: reconnect per job (never hold an idle socket); TCP
// connect is the primary health signal; DLE EOT status is best effort and
// never gates sending; any failure is an *Error so the worker queues and
// retries.
package transport

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/ninondev/stove/internal/config"
	"github.com/ninondev/stove/print/escpos"
)

// Error is TransportError: the payload could not be delivered.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// StatusResult is the outcome of a probe. Reason is the Chinese text for the
// alert popup (nil = OK or unknown); PaperOK/CoverOK nil = unknown.
type StatusResult struct {
	Reachable bool
	Reason    *string
	PaperOK   *bool
	CoverOK   *bool
}

func reason(s string) *string { return &s }

func boolP(b bool) *bool { return &b }

// Transport sends one payload or probes the printer.
type Transport interface {
	Send(payload []byte) error
	Probe() StatusResult
	Describe() string
}

// ErrNoSpooler: this host is not Windows, so it has no installed-printer
// list.
var ErrNoSpooler = errors.New("no windows spooler on this host")

// LocalPrinter is one printer installed on the host.
type LocalPrinter struct {
	Name    string
	Port    string
	Driver  string
	Default bool
}

// osErrorText renders a Go network/OS error the way Python's str(OSError) did
// in the earlier version of this kit, for the common cases ("timed out", "[Errno 61] Connection refused").
func osErrorText(err error) string {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timed out"
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return "timed out"
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errnoText(errno)
	}
	return err.Error()
}

// errnoText is str(OSError(errno, strerror)): "[Errno 2] No such file or
// directory" (C strerror text starts with a capital; Go's is lower case),
// or "[WinError 5] <system message>" on Windows.
func errnoText(errno syscall.Errno) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("[WinError %d] %s", uintptr(errno), errno.Error())
	}
	return fmt.Sprintf("[Errno %d] %s", uintptr(errno), capitalize(errno.Error()))
}

func capitalize(s string) string {
	if s != "" && s[0] >= 'a' && s[0] <= 'z' {
		return string(s[0]-('a'-'A')) + s[1:]
	}
	return s
}

// --- network (raw TCP 9100) ---------------------------------------------------

// Network is NetworkTransport.
type Network struct {
	Host           string
	Port           int64
	ConnectTimeout time.Duration
	WriteTimeout   time.Duration
}

// NewNetwork uses the configured timeouts.
func NewNetwork(host string, port int64) *Network {
	return &Network{Host: host, Port: port, ConnectTimeout: config.PrinterConnectTimeout,
		WriteTimeout: config.PrinterWriteTimeout}
}

func (n *Network) Describe() string { return fmt.Sprintf("%s:%d", n.Host, n.Port) }

func (n *Network) addr() string { return net.JoinHostPort(n.Host, strconv.FormatInt(n.Port, 10)) }

func (n *Network) Send(payload []byte) error {
	conn, err := net.DialTimeout("tcp", n.addr(), n.ConnectTimeout)
	if err != nil {
		return &Error{fmt.Sprintf("网络打印失败 %s:%d: %s", n.Host, n.Port, osErrorText(err))}
	}
	defer conn.Close()
	if err := conn.SetWriteDeadline(time.Now().Add(n.WriteTimeout)); err != nil {
		return &Error{fmt.Sprintf("网络打印失败 %s:%d: %s", n.Host, n.Port, osErrorText(err))}
	}
	if _, err := conn.Write(payload); err != nil {
		return &Error{fmt.Sprintf("网络打印失败 %s:%d: %s", n.Host, n.Port, osErrorText(err))}
	}
	return nil
}

// Probe: connect is the trustworthy liveness check; DLE EOT is best effort.
func (n *Network) Probe() StatusResult {
	conn, err := net.DialTimeout("tcp", n.addr(), n.ConnectTimeout)
	if err != nil {
		return StatusResult{Reachable: false, Reason: reason("离线/连接失败")}
	}
	defer conn.Close()
	return readStatus(conn)
}

// readOne is s.recv(1) with a 1s timeout: (byte, got, error).
func readOne(conn net.Conn) (byte, bool, error) {
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	var b [1]byte
	n, err := conn.Read(b[:])
	if n == 1 {
		return b[0], true, nil
	}
	if err == io.EOF {
		return 0, false, nil // orderly close: recv() returns b""
	}
	if err == nil {
		return 0, false, nil
	}
	return 0, false, err
}

func sendAll(conn net.Conn, p []byte) error {
	_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
	_, err := conn.Write(p)
	return err
}

// readStatus is _read_status: no answer means unknown (still reachable).
func readStatus(conn net.Conn) StatusResult {
	var why *string
	var paperOK, coverOK *bool
	func() {
		if err := sendAll(conn, escpos.StatusQuery(4)); err != nil {
			return
		}
		b, got, err := readOne(conn)
		if err != nil {
			return
		}
		if got {
			if b&0x60 != 0 { // bits 5&6: paper end
				paperOK = boolP(false)
				why = reason("缺纸")
			} else {
				paperOK = boolP(true)
			}
		}
		if err := sendAll(conn, escpos.StatusQuery(2)); err != nil {
			return
		}
		b, got, err = readOne(conn)
		if err != nil {
			return
		}
		if got {
			if b&0x04 != 0 { // bit 2: cover open
				coverOK = boolP(false)
				if why != nil {
					why = reason("开盖或缺纸")
				} else {
					why = reason("开盖")
				}
			} else {
				coverOK = boolP(true)
			}
		}
	}()
	return StatusResult{Reachable: true, Reason: why, PaperOK: paperOK, CoverOK: coverOK}
}

// --- serial ---------------------------------------------------------------------

// Serial is SerialTransport: a local serial device (/dev/ttyUSB0, COM3).
type Serial struct {
	Path         string
	Baud         int64
	WriteTimeout time.Duration
}

func NewSerial(path string, baud int64) *Serial {
	return &Serial{Path: path, Baud: baud, WriteTimeout: config.PrinterWriteTimeout}
}

func (s *Serial) Describe() string { return s.Path }

func (s *Serial) Send(payload []byte) error {
	if err := serialWrite(s.Path, s.Baud, s.WriteTimeout, payload); err != nil {
		return &Error{fmt.Sprintf("串口打印失败 %s: %s", s.Path, err.Error())}
	}
	return nil
}

func (s *Serial) Probe() StatusResult { return serialProbe(s.Path) }

// --- fake -------------------------------------------------------------------------

// Fake is FakeTransport: an in-memory printer for tests and "null" stations.
type Fake struct {
	Name   string
	mu     sync.Mutex
	online bool
	sent   [][]byte
}

func NewFake(name string, online bool) *Fake { return &Fake{Name: name, online: online} }

func (f *Fake) Describe() string { return "fake:" + f.Name }

// SetOnline toggles failure.
func (f *Fake) SetOnline(on bool) {
	f.mu.Lock()
	f.online = on
	f.mu.Unlock()
}

// Sent returns copies of the payloads received.
func (f *Fake) Sent() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]byte, len(f.sent))
	for i, p := range f.sent {
		out[i] = append([]byte(nil), p...)
	}
	return out
}

func (f *Fake) Send(payload []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.online {
		return &Error{"假打印机离线 " + f.Name}
	}
	f.sent = append(f.sent, append([]byte(nil), payload...))
	return nil
}

func (f *Fake) Probe() StatusResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.online {
		return StatusResult{Reachable: true}
	}
	return StatusResult{Reachable: false, Reason: reason("离线/连接失败")}
}

// --- factory ----------------------------------------------------------------------

// ConfigError is the ValueError build_transport raises for a station row
// that cannot be used (its text goes into the 配置错误 health reason).
type ConfigError struct{ Msg string }

func (e *ConfigError) Error() string { return e.Msg }

// Build is build_transport, from a stations row's transport columns.
// "winspool" sends to the Windows printer named in serial_path.
func Build(kind string, netHost *string, netPort int64, serialPath *string, serialBaud int64) (Transport, error) {
	switch kind {
	case "net":
		if netHost == nil || *netHost == "" {
			return nil, &ConfigError{"网口出单口缺少 IP 地址"}
		}
		return NewNetwork(*netHost, netPort), nil
	case "serial":
		if serialPath == nil || *serialPath == "" {
			return nil, &ConfigError{"串口出单口缺少设备路径"}
		}
		return NewSerial(*serialPath, serialBaud), nil
	case "null":
		return NewFake("null", true), nil
	case "winspool":
		if serialPath == nil || *serialPath == "" {
			return nil, &ConfigError{"Windows 打印机出单口缺少打印机名称"}
		}
		return newWinspool(*serialPath)
	}
	return nil, &ConfigError{"未知传输方式: " + kind}
}
