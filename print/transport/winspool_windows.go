//go:build windows

// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

package transport

// Winspool sends RAW ESC/POS bytes to an installed Windows printer by name
// (the spooler passes them through untouched, so cut and drawer-kick
// commands work). No cgo: winspool.drv is loaded lazily from System32.

import (
	"fmt"
	"sort"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modWinspool          = windows.NewLazySystemDLL("winspool.drv")
	procOpenPrinterW     = modWinspool.NewProc("OpenPrinterW")
	procClosePrinter     = modWinspool.NewProc("ClosePrinter")
	procStartDocPrinterW = modWinspool.NewProc("StartDocPrinterW")
	procStartPagePrinter = modWinspool.NewProc("StartPagePrinter")
	procWritePrinter     = modWinspool.NewProc("WritePrinter")
	procEndPagePrinter   = modWinspool.NewProc("EndPagePrinter")
	procEndDocPrinter    = modWinspool.NewProc("EndDocPrinter")
	procAbortPrinter     = modWinspool.NewProc("AbortPrinter")
	procGetPrinterW      = modWinspool.NewProc("GetPrinterW")
	procEnumPrintersW    = modWinspool.NewProc("EnumPrintersW")
	procGetDefaultPrnW   = modWinspool.NewProc("GetDefaultPrinterW")
)

// winspoolReady loads winspool.drv and finds every procedure once, so a
// missing one is an error instead of a panic inside LazyProc.Call.
var winspoolReady = func() func() error {
	var once sync.Once
	var err error
	return func() error {
		once.Do(func() {
			for _, p := range []*windows.LazyProc{procOpenPrinterW, procClosePrinter, procStartDocPrinterW,
				procStartPagePrinter, procWritePrinter, procEndPagePrinter, procEndDocPrinter,
				procAbortPrinter, procGetPrinterW} {
				if err = p.Find(); err != nil {
					return
				}
			}
		})
		return err
	}
}()

// docInfo1 is DOC_INFO_1W.
type docInfo1 struct {
	DocName    *uint16
	OutputFile *uint16
	Datatype   *uint16
}

// printerInfo2 is PRINTER_INFO_2W.
type printerInfo2 struct {
	ServerName, PrinterName, ShareName, PortName, DriverName, Comment, Location           uintptr
	DevMode, SepFile, PrintProcessor, Datatype, Parameters, SecurityDescriptor            uintptr
	Attributes, Priority, DefaultPriority, StartTime, UntilTime, Status, Jobs, AveragePPM uint32
}

// PRINTER_STATUS_* and PRINTER_ATTRIBUTE_WORK_OFFLINE.
const (
	psPaused           = 0x00000001
	psError            = 0x00000002
	psPaperJam         = 0x00000008
	psPaperOut         = 0x00000010
	psPaperProblem     = 0x00000040
	psOffline          = 0x00000080
	psNotAvailable     = 0x00001000
	psUserIntervention = 0x00100000
	psDoorOpen         = 0x00400000
	psServerUnknown    = 0x00800000
	paWorkOffline      = 0x00000400
)

// Winspool is a printer installed in Windows ("控制面板 → 设备和打印机").
type Winspool struct{ Name string }

func newWinspool(name string) (Transport, error) { return &Winspool{Name: name}, nil }

func (w *Winspool) Describe() string { return "winspool:" + w.Name }

func (w *Winspool) fail(err error) error {
	return &Error{fmt.Sprintf("Windows 打印失败 %s: %s", w.Name, winErrText(err))}
}

func (w *Winspool) open() (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(w.Name)
	if err != nil {
		return 0, err
	}
	var h windows.Handle
	r, _, e := procOpenPrinterW.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&h)), 0)
	if r == 0 {
		return 0, lastErr(e)
	}
	return h, nil
}

// lastErr turns a LazyProc.Call error into one worth printing.
func lastErr(e error) error {
	if errno, ok := e.(windows.Errno); ok && errno == 0 {
		return windows.ERROR_GEN_FAILURE
	}
	return e
}

func (w *Winspool) Send(payload []byte) (err error) {
	if err := winspoolReady(); err != nil {
		return &Error{fmt.Sprintf("Windows 打印失败 %s: %v", w.Name, err)}
	}
	h, err := w.open()
	if err != nil {
		return w.fail(err)
	}
	defer procClosePrinter.Call(uintptr(h))

	doc, _ := windows.UTF16PtrFromString("stove")
	raw, _ := windows.UTF16PtrFromString("RAW")
	di := docInfo1{DocName: doc, Datatype: raw}
	r, _, e := procStartDocPrinterW.Call(uintptr(h), 1, uintptr(unsafe.Pointer(&di)))
	if r == 0 {
		return w.fail(lastErr(e))
	}
	// A job that failed half way is deleted, not finished: a cut-off ticket
	// followed by the retry would print the head twice.
	finished := false
	defer func() {
		if !finished {
			procAbortPrinter.Call(uintptr(h))
		}
	}()
	if r, _, e := procStartPagePrinter.Call(uintptr(h)); r == 0 {
		return w.fail(lastErr(e))
	}
	for off := 0; off < len(payload); {
		var n uint32
		chunk := payload[off:]
		r, _, e := procWritePrinter.Call(uintptr(h), uintptr(unsafe.Pointer(&chunk[0])),
			uintptr(uint32(len(chunk))), uintptr(unsafe.Pointer(&n)))
		if r == 0 {
			return w.fail(lastErr(e))
		}
		if n == 0 {
			return w.fail(windows.ERROR_WRITE_FAULT)
		}
		off += int(n)
	}
	if r, _, e := procEndPagePrinter.Call(uintptr(h)); r == 0 {
		return w.fail(lastErr(e))
	}
	if r, _, e := procEndDocPrinter.Call(uintptr(h)); r == 0 {
		return w.fail(lastErr(e))
	}
	finished = true
	return nil
}

// Probe asks the spooler for the printer's status (GetPrinter level 2). The
// spooler only knows what the driver reports, so "no error" can also mean
// "unknown"; that counts as reachable, like a silent DLE EOT.
func (w *Winspool) Probe() StatusResult {
	if winspoolReady() != nil {
		return StatusResult{Reachable: false, Reason: reason("离线/找不到打印机")}
	}
	h, err := w.open()
	if err != nil {
		return StatusResult{Reachable: false, Reason: reason("离线/找不到打印机")}
	}
	defer procClosePrinter.Call(uintptr(h))
	var need uint32
	procGetPrinterW.Call(uintptr(h), 2, 0, 0, uintptr(unsafe.Pointer(&need)))
	if need < uint32(unsafe.Sizeof(printerInfo2{})) {
		return StatusResult{Reachable: true}
	}
	buf := make([]uint64, (need+7)/8) // 8-byte aligned
	r, _, _ := procGetPrinterW.Call(uintptr(h), 2, uintptr(unsafe.Pointer(&buf[0])), uintptr(need),
		uintptr(unsafe.Pointer(&need)))
	if r == 0 {
		return StatusResult{Reachable: true}
	}
	info := (*printerInfo2)(unsafe.Pointer(&buf[0]))
	if why := spoolerReason(info.Status, info.Attributes); why != "" {
		return StatusResult{Reachable: false, Reason: reason(why)}
	}
	return StatusResult{Reachable: true}
}

// spoolerReason maps PRINTER_INFO_2 Status/Attributes to the short Chinese
// alert text ("" = no problem).
func spoolerReason(status, attrs uint32) string {
	switch {
	case status&(psOffline|psNotAvailable|psServerUnknown) != 0 || attrs&paWorkOffline != 0:
		return "离线/未插好"
	case status&(psPaperOut|psPaperProblem) != 0:
		return "缺纸"
	case status&psDoorOpen != 0:
		return "开盖"
	case status&psPaperJam != 0:
		return "卡纸"
	case status&psPaused != 0:
		return "已暂停"
	case status&(psError|psUserIntervention) != 0:
		return "故障/需人工处理"
	}
	return ""
}

// PRINTER_ENUM_LOCAL | PRINTER_ENUM_CONNECTIONS.
const enumLocalPrinters = 0x00000002 | 0x00000004

// info2String reads one of PRINTER_INFO_2W's string pointers. It goes through
// a pointer indirection because a direct uintptr-to-unsafe.Pointer
// conversion is what go vet's unsafeptr check rejects.
func info2String(p uintptr) string {
	if p == 0 {
		return ""
	}
	return windows.UTF16PtrToString(*(**uint16)(unsafe.Pointer(&p)))
}

// defaultPrinterName is GetDefaultPrinterW, "" when there is none.
func defaultPrinterName() string {
	var n uint32
	procGetDefaultPrnW.Call(0, uintptr(unsafe.Pointer(&n)))
	if n == 0 {
		return ""
	}
	buf := make([]uint16, n)
	r, _, _ := procGetDefaultPrnW.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)))
	if r == 0 {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

// LocalPrinters lists every printer installed on this host, sorted by name
// and unfiltered: virtual printers (PDF, XPS, fax) are listed too. EnumPrintersW is called twice: first with a nil buffer to learn the
// size, then for the data.
func LocalPrinters() ([]LocalPrinter, error) {
	if err := winspoolReady(); err != nil {
		return nil, err
	}
	def := defaultPrinterName()
	var need uint32
	procEnumPrintersW.Call(uintptr(enumLocalPrinters), 0, 2, 0, 0,
		uintptr(unsafe.Pointer(&need)), 0)
	if need == 0 {
		return []LocalPrinter{}, nil
	}
	buf := make([]uint64, (need+7)/8) // 8-byte aligned, as GetPrinter above
	var count uint32
	r, _, e := procEnumPrintersW.Call(uintptr(enumLocalPrinters), 0, 2,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(need),
		uintptr(unsafe.Pointer(&need)), uintptr(unsafe.Pointer(&count)))
	if r == 0 {
		return nil, lastErr(e)
	}
	out := make([]LocalPrinter, 0, count)
	base := unsafe.Pointer(&buf[0])
	size := unsafe.Sizeof(printerInfo2{})
	for i := uint32(0); i < count; i++ {
		info := (*printerInfo2)(unsafe.Add(base, uintptr(i)*size))
		name := info2String(info.PrinterName)
		out = append(out, LocalPrinter{
			Name:    name,
			Port:    info2String(info.PortName),
			Driver:  info2String(info.DriverName),
			Default: def != "" && name == def,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
