// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

package transport

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func strp(s string) *string { return &s }

func derefS(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func derefB(p *bool) string {
	if p == nil {
		return "<nil>"
	}
	return strconv.FormatBool(*p)
}

// fakePrinter is a raw-9100 listener: it answers DLE EOT queries from
// answers (nil = stay silent) and records every connection's other bytes.
type fakePrinter struct {
	ln      net.Listener
	answers map[byte]byte
	mu      sync.Mutex
	jobs    [][]byte
	wg      sync.WaitGroup
}

func newFakePrinter(t *testing.T, answers map[byte]byte) *fakePrinter {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &fakePrinter{ln: ln, answers: answers}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			p.wg.Add(1)
			go p.handle(c)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return p
}

func (p *fakePrinter) handle(c net.Conn) {
	defer p.wg.Done()
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	var buf []byte
	tmp := make([]byte, 4096)
	for {
		n, err := c.Read(tmp)
		buf = append(buf, tmp[:n]...)
		for len(buf) >= 3 && buf[0] == 0x10 && buf[1] == 0x04 {
			if a, ok := p.answers[buf[2]]; ok {
				_, _ = c.Write([]byte{a})
			}
			buf = buf[3:]
		}
		if err != nil {
			break
		}
	}
	if len(buf) > 0 {
		p.mu.Lock()
		p.jobs = append(p.jobs, buf)
		p.mu.Unlock()
	}
}

func (p *fakePrinter) port() int64 { return int64(p.ln.Addr().(*net.TCPAddr).Port) }

func (p *fakePrinter) waitJobs(t *testing.T, n int) [][]byte {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		got := len(p.jobs)
		p.mu.Unlock()
		if got >= n {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([][]byte(nil), p.jobs...)
}

func netTransport(port int64) *Network {
	return &Network{Host: "127.0.0.1", Port: port, ConnectTimeout: time.Second, WriteTimeout: time.Second}
}

func TestNetworkSendDeliversBytes(t *testing.T) {
	p := newFakePrinter(t, nil)
	tr := netTransport(p.port())
	payload := []byte("\x1b@测试\n\x1bd\x01\x1dVB\x03\x1bp\x00\x19\xfa")
	if err := tr.Send(payload); err != nil {
		t.Fatal(err)
	}
	jobs := p.waitJobs(t, 1)
	if len(jobs) != 1 || !bytes.Equal(jobs[0], payload) {
		t.Fatalf("printer got %q, want %q", jobs, payload)
	}
	if got := tr.Describe(); got != "127.0.0.1:"+strconv.FormatInt(p.port(), 10) {
		t.Fatalf("Describe = %q", got)
	}
}

func closedPort(t *testing.T) int64 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := int64(ln.Addr().(*net.TCPAddr).Port)
	ln.Close()
	return port
}

func TestNetworkSendRefused(t *testing.T) {
	port := closedPort(t)
	err := netTransport(port).Send([]byte("x"))
	te, ok := err.(*Error)
	if !ok {
		t.Fatalf("err = %#v, want *Error", err)
	}
	prefix := "网络打印失败 127.0.0.1:" + strconv.FormatInt(port, 10) + ": [Errno "
	if !strings.HasPrefix(te.Msg, prefix) || !strings.HasSuffix(te.Msg, "] Connection refused") {
		t.Fatalf("message %q, want %q...] Connection refused", te.Msg, prefix)
	}
	st := netTransport(port).Probe()
	if st.Reachable || derefS(st.Reason) != "离线/连接失败" || st.PaperOK != nil || st.CoverOK != nil {
		t.Fatalf("probe = %+v reason %s", st, derefS(st.Reason))
	}
}

func TestNetworkProbeStatus(t *testing.T) {
	cases := []struct {
		name         string
		answers      map[byte]byte
		reason       string
		paper, cover string
	}{
		{"healthy", map[byte]byte{4: 0x12, 2: 0x12}, "<nil>", "true", "true"},
		{"paper end", map[byte]byte{4: 0x72, 2: 0x12}, "缺纸", "false", "true"},
		{"cover open", map[byte]byte{4: 0x12, 2: 0x16}, "开盖", "true", "false"},
		{"both", map[byte]byte{4: 0x60, 2: 0x04}, "开盖或缺纸", "false", "false"},
		{"silent", nil, "<nil>", "<nil>", "<nil>"},
		{"paper only answers", map[byte]byte{4: 0x12}, "<nil>", "true", "<nil>"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := newFakePrinter(t, c.answers)
			st := netTransport(p.port()).Probe()
			if !st.Reachable || derefS(st.Reason) != c.reason || derefB(st.PaperOK) != c.paper ||
				derefB(st.CoverOK) != c.cover {
				t.Fatalf("probe = reachable %v reason %s paper %s cover %s", st.Reachable,
					derefS(st.Reason), derefB(st.PaperOK), derefB(st.CoverOK))
			}
		})
	}
}

// A printer that accepts the connection but never reads: the write deadline
// turns into "timed out" instead of hanging the worker.
func TestNetworkWriteTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var held []net.Conn
	var mu sync.Mutex
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, c)
			mu.Unlock()
		}
	}()
	defer func() {
		mu.Lock()
		for _, c := range held {
			c.Close()
		}
		mu.Unlock()
	}()
	tr := &Network{Host: "127.0.0.1", Port: int64(ln.Addr().(*net.TCPAddr).Port),
		ConnectTimeout: time.Second, WriteTimeout: 300 * time.Millisecond}
	start := time.Now()
	err = tr.Send(bytes.Repeat([]byte("x"), 64<<20))
	if err == nil || !strings.HasSuffix(err.Error(), ": timed out") {
		t.Fatalf("err = %v, want ...: timed out", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("send took %v", time.Since(start))
	}
}

func TestFake(t *testing.T) {
	f := NewFake("前台", true)
	if err := f.Send([]byte("a")); err != nil {
		t.Fatal(err)
	}
	if st := f.Probe(); !st.Reachable || st.Reason != nil {
		t.Fatalf("online probe = %+v", st)
	}
	f.SetOnline(false)
	err := f.Send([]byte("b"))
	if err == nil || err.Error() != "假打印机离线 前台" {
		t.Fatalf("err = %v", err)
	}
	if st := f.Probe(); st.Reachable || derefS(st.Reason) != "离线/连接失败" {
		t.Fatalf("offline probe = %+v", st)
	}
	if got := f.Sent(); len(got) != 1 || string(got[0]) != "a" {
		t.Fatalf("sent = %q", got)
	}
	if f.Describe() != "fake:前台" {
		t.Fatalf("Describe = %q", f.Describe())
	}
}

func TestBuild(t *testing.T) {
	cases := []struct {
		kind, host, path string
		want             string // Describe, or "err:" + message
	}{
		{"net", "192.0.2.5", "", "192.0.2.5:9100"},
		{"net", "", "", "err:网口出单口缺少 IP 地址"},
		{"serial", "", "/dev/ttyUSB0", "/dev/ttyUSB0"},
		{"serial", "", "", "err:串口出单口缺少设备路径"},
		{"null", "", "", "fake:null"},
		{"winspool", "", "", "err:Windows 打印机出单口缺少打印机名称"},
		{"usb", "", "", "err:未知传输方式: usb"},
		{"", "", "", "err:未知传输方式: "},
	}
	for _, c := range cases {
		var host, path *string
		if c.host != "" {
			host = strp(c.host)
		}
		if c.path != "" {
			path = strp(c.path)
		}
		tr, err := Build(c.kind, host, 9100, path, 115200)
		got := ""
		if err != nil {
			if _, ok := err.(*ConfigError); !ok {
				t.Errorf("%s: error type %T", c.kind, err)
			}
			got = "err:" + err.Error()
		} else {
			got = tr.Describe()
		}
		if got != c.want {
			t.Errorf("Build(%q, %q, %q) = %q, want %q", c.kind, c.host, c.path, got, c.want)
		}
	}
	// An empty string counts as missing, like Python's `if not net_host`.
	if _, err := Build("net", strp(""), 9100, nil, 0); err == nil {
		t.Error("empty host accepted")
	}
}

func TestSerialProbeMissing(t *testing.T) {
	st := NewSerial(filepath.Join(t.TempDir(), "ttyNOPE"), 9600).Probe()
	if st.Reachable || derefS(st.Reason) != "离线/未插好" {
		t.Fatalf("probe = %+v reason %s", st, derefS(st.Reason))
	}
}

func TestSerialSendMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ttyNOPE")
	err := NewSerial(path, 9600).Send([]byte("x"))
	if err == nil {
		t.Fatal("send to a missing device succeeded")
	}
	if _, ok := err.(*Error); !ok || !strings.HasPrefix(err.Error(), "串口打印失败 "+path+": ") {
		t.Fatalf("err = %v", err)
	}
}

func TestWinspoolOffWindows(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("windows")
	}
	_, err := Build("winspool", nil, 9100, strp("POS-80"), 0)
	if err == nil || err.Error() != "本机不是 Windows，不能用 winspool 打印机" {
		t.Fatalf("err = %v", err)
	}
}
