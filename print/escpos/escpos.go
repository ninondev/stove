// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

// Package escpos is a hand-rolled ESC/POS byte layer.
//
// Chinese handling: ESC @ -> FS & -> FS C 0 (GBK) -> GB18030 text. Layout is
// 80mm Font A = config.PrintColumns half-width columns; East Asian wide and
// fullwidth characters take 2 columns (eaw_table.go, generated from Python's
// unicodedata, which the earlier version of this kit used).
package escpos

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"

	"github.com/ninondev/stove/internal/config"
	"github.com/ninondev/stove/internal/pyfmt"
)

// Raw command constants. They are slices: never append to them in place.
var (
	ESC = []byte{0x1b}
	GS  = []byte{0x1d}
	FS  = []byte{0x1c}
	DLE = []byte{0x10}

	CmdInit      = []byte{0x1b, 0x40}             // ESC @  initialise
	CmdKanjiOn   = []byte{0x1c, 0x26}             // FS &   enter Chinese mode
	CmdKanjiOff  = []byte{0x1c, 0x2e}             // FS .   exit Chinese mode
	CmdSelectGBK = []byte{0x1c, 0x43, 0x00}       // FS C 0 select GBK table
	CmdCut       = []byte{0x1d, 0x56, 0x42, 0x03} // GS V 66 3: feed 3 lines, partial cut
	CmdFeed1     = []byte{0x1b, 0x64, 0x01}       // ESC d 1 (the serial-injection anchor)
	// cash drawer kick: ESC p m t1=25 t2=250
	CmdKickPin2 = []byte{0x1b, 0x70, 0x00, 0x19, 0xfa}
	CmdKickPin5 = []byte{0x1b, 0x70, 0x01, 0x19, 0xfa}
)

// cat returns a fresh concatenation (never aliases its arguments).
func cat(parts ...[]byte) []byte {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// runeWidth is 2 for East Asian Wide/Fullwidth code points, else 1.
func runeWidth(r rune) int {
	i := sort.Search(len(eawWide), func(i int) bool { return eawWide[i][1] >= r })
	if i < len(eawWide) && eawWide[i][0] <= r {
		return 2
	}
	return 1
}

// DisplayWidth is display_width: printed column width of s.
func DisplayWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeWidth(r)
	}
	return w
}

// TruncateToWidth is truncate_to_width.
func TruncateToWidth(s string, width int) string {
	w := 0
	for i, r := range s {
		cw := runeWidth(r)
		if w+cw > width {
			return s[:i]
		}
		w += cw
	}
	return s
}

// StatusQuery is DLE EOT n (n=1 printer, 2 offline, 4 paper).
func StatusQuery(n byte) []byte { return []byte{0x10, 0x04, n} }

// StripCtrl is _strip_ctrl: drop C0 controls (except \n and \t), DEL and C1,
// so user text can never smuggle printer commands into a ticket.
func StripCtrl(s string) string {
	clean := true
	for _, r := range s {
		if !keepRune(r) {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if keepRune(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func keepRune(r rune) bool {
	if r == '\n' || r == '\t' {
		return true
	}
	return r >= 0x20 && !(r >= 0x7f && r <= 0x9f)
}

// EncodeGB18030 is s.encode("gb18030", errors="replace") with CPython's
// mapping (two-byte user-defined areas for the Private Use Area).
func EncodeGB18030(s string) []byte {
	out := make([]byte, 0, len(s)*2)
	var run []byte // pending UTF-8 for x/text
	flush := func() {
		if len(run) == 0 {
			return
		}
		enc := simplifiedchinese.GB18030.NewEncoder()
		if b, err := enc.Bytes(run); err == nil {
			out = append(out, b...)
		} else {
			// errors="replace": encode rune by rune, '?' for what fails.
			for _, r := range string(run) {
				if b, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(string(r))); err == nil {
					out = append(out, b...)
				} else {
					out = append(out, '?')
				}
			}
		}
		run = run[:0]
	}
	for _, r := range s {
		if r < 0x80 {
			flush()
			out = append(out, byte(r))
			continue
		}
		if v, ok := gbPUA[r]; ok {
			flush()
			out = append(out, byte(v>>8), byte(v))
			continue
		}
		run = utf8.AppendRune(run, r)
	}
	flush()
	return out
}

// spaces is " " * n in Python (empty for n <= 0).
func spaces(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat(" ", n)
}

// Builder accumulates an ESC/POS byte stream for one ticket.
type Builder struct {
	Columns int
	buf     []byte
}

// NewBuilder is Builder() with config.PrintColumns columns.
func NewBuilder() *Builder { return &Builder{Columns: config.PrintColumns} }

// NewBuilderCols is Builder(columns=n).
func NewBuilderCols(n int) *Builder { return &Builder{Columns: n} }

func (b *Builder) Raw(p []byte) *Builder {
	b.buf = append(b.buf, p...)
	return b
}

func (b *Builder) Init() *Builder { return b.Raw(CmdInit).Raw(CmdKanjiOn).Raw(CmdSelectGBK) }

// Text appends user text: control bytes stripped, GB18030-encoded.
func (b *Builder) Text(s string) *Builder { return b.Raw(EncodeGB18030(StripCtrl(s))) }

// Line is text(s) + "\n".
func (b *Builder) Line(s string) *Builder { return b.Text(s).Raw([]byte{'\n'}) }

// Align: 0 left, 1 center, 2 right.
func (b *Builder) Align(mode int) *Builder { return b.Raw([]byte{0x1b, 0x61, byte(mode)}) }

func (b *Builder) Bold(on bool) *Builder {
	v := byte(0)
	if on {
		v = 1
	}
	return b.Raw([]byte{0x1b, 0x45, v})
}

func clampI(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Size is GS ! n: width/height multipliers 1..8.
func (b *Builder) Size(w, h int) *Builder {
	w = clampI(w, 1, 8)
	h = clampI(h, 1, 8)
	return b.Raw([]byte{0x1d, 0x21, byte(((w - 1) << 4) | (h - 1))})
}

// Feed is ESC d n.
func (b *Builder) Feed(n int) *Builder { return b.Raw([]byte{0x1b, 0x64, byte(clampI(n, 0, 255))}) }

// HR is hr(): a full-width line of "-".
func (b *Builder) HR() *Builder { return b.HRChar("-") }

// HRChar is hr(char).
func (b *Builder) HRChar(ch string) *Builder {
	if b.Columns <= 0 {
		return b.Line("")
	}
	return b.Line(strings.Repeat(ch, b.Columns))
}

func (b *Builder) Center(s string) *Builder { return b.Align(1).Line(s).Align(0) }

// TwoCol is two_col: left text + right-aligned text on one line, width-aware;
// the left side is truncated when they collide.
func (b *Builder) TwoCol(left, right string) *Builder {
	rw := DisplayWidth(right)
	avail := b.Columns - rw - 1
	if avail < 1 {
		return b.Line(left).Align(2).Line(right).Align(0)
	}
	left = TruncateToWidth(left, avail)
	gap := b.Columns - DisplayWidth(left) - rw
	if gap < 1 {
		gap = 1
	}
	return b.Line(left + spaces(gap) + right)
}

// ThreeCol is three_col(left, mid, right) with the default 10/10 widths.
func (b *Builder) ThreeCol(left, mid, right string) *Builder {
	return b.ThreeColW(left, mid, right, 10, 10)
}

// ThreeColW is three_col with explicit mid_w / right_w.
func (b *Builder) ThreeColW(left, mid, right string, midW, rightW int) *Builder {
	nameW := b.Columns - midW - rightW
	left = TruncateToWidth(left, nameW)
	leftPad := left + spaces(nameW-DisplayWidth(left))
	var midPad, rightPad string
	if DisplayWidth(mid) <= midW {
		midPad = spaces(midW-DisplayWidth(mid)) + mid
	} else {
		midPad = TruncateToWidth(mid, midW)
	}
	if DisplayWidth(right) <= rightW {
		rightPad = spaces(rightW-DisplayWidth(right)) + right
	} else {
		rightPad = TruncateToWidth(right, rightW)
	}
	return b.Line(leftPad + midPad + rightPad)
}

// Cut feeds one line then cuts.
func (b *Builder) Cut() *Builder { return b.Feed(1).Raw(CmdCut) }

// Kick appends the drawer kick (pin 2, or pin 5).
func (b *Builder) Kick(pin5 bool) *Builder {
	if pin5 {
		return b.Raw(CmdKickPin5)
	}
	return b.Raw(CmdKickPin2)
}

// Build returns a copy of the bytes so far.
func (b *Builder) Build() []byte { return append([]byte(nil), b.buf...) }

// SerialFooterLine is the footer text f"流水号 No.{serial%100000:05d}".
func SerialFooterLine(serial int64) string {
	return fmt.Sprintf("流水号 No.%05d", pyfmt.Mod(serial, 100000))
}

// SerialFooterBytes is serial_footer_bytes: the exact bytes render's
// b.line(serial footer) produces (text + "\n").
func SerialFooterBytes(serial int64) []byte {
	return append(EncodeGB18030(SerialFooterLine(serial)), '\n')
}

// InjectSerial adds the serial footer to a payload rendered
// without one, right before the final feed+cut (falling back to before the
// cut, then to appending), byte-identical to rendering with that serial.
func InjectSerial(payload []byte, serial int64) []byte {
	line := SerialFooterBytes(serial)
	idx := bytes.LastIndex(payload, cat(CmdFeed1, CmdCut))
	if idx < 0 {
		idx = bytes.LastIndex(payload, CmdCut)
	}
	if idx < 0 {
		return cat(payload, line)
	}
	return cat(payload[:idx], line, payload[idx:])
}

// StripTrailingKick removes one trailing drawer kick (PIN2 checked first,
// then PIN5), as recovery, retry, reprint and the late-kick rule do.
func StripTrailingKick(payload []byte) ([]byte, bool) {
	for _, k := range [][]byte{CmdKickPin2, CmdKickPin5} {
		if bytes.HasSuffix(payload, k) {
			out := make([]byte, len(payload)-len(k))
			copy(out, payload)
			return out, true
		}
	}
	return payload, false
}

// InitPrefix is the bytes Builder.Init() emits at the head of
// every ticket.
var InitPrefix = cat(CmdInit, CmdKanjiOn, CmdSelectGBK)
