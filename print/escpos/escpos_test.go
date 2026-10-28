// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

package escpos

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

func load(t *testing.T, name string, v any) {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

func hx(b []byte) string { return hex.EncodeToString(b) }

// Every code point: the GB18030 bytes and the wide/narrow flag hash to the
// values recorded from the earlier Python version of this kit.
func TestTables(t *testing.T) {
	var all escposCases
	load(t, "escpos_cases.json", &all)
	want := all.Tables
	enc := sha256.New()
	wf := sha256.New()
	for cp := rune(0); cp < 0x110000; cp++ {
		if cp >= 0xD800 && cp <= 0xDFFF {
			continue
		}
		b := EncodeGB18030(string(cp))
		enc.Write([]byte{byte(len(b))})
		enc.Write(b)
		if runeWidth(cp) == 2 {
			wf.Write([]byte{1})
		} else {
			wf.Write([]byte{0})
		}
	}
	if got := hx(enc.Sum(nil)); got != want.GB {
		t.Errorf("gb18030 hash %s, recorded %s", got, want.GB)
	}
	if got := hx(wf.Sum(nil)); got != want.Wide {
		t.Errorf("east asian width hash %s, recorded %s", got, want.Wide)
	}
}

type escposCases struct {
	Tables struct {
		GB   string `json:"gb18030_sha256"`
		Wide string `json:"wide_sha256"`
	} `json:"tables"`
	Texts []struct {
		S       string   `json:"s"`
		Width   int      `json:"width"`
		Strip   string   `json:"strip"`
		Trunc   []string `json:"trunc"`
		TextHex string   `json:"text_hex"`
	} `json:"texts"`
	Cols []struct {
		Columns int    `json:"columns"`
		Op      string `json:"op"`
		Args    []any  `json:"args"`
		Hex     string `json:"hex"`
	} `json:"cols"`
	Misc struct {
		Size        [][3]any `json:"size"`
		Feed        [][2]any `json:"feed"`
		Align       [][2]any `json:"align"`
		Bold        [][2]any `json:"bold"`
		Center      string   `json:"center"`
		InitCutKick string   `json:"init_cut_kick"`
		StatusQuery []string `json:"status_query"`
	} `json:"misc"`
	Serials []struct {
		Serial    int64  `json:"serial"`
		FooterHex string `json:"footer_hex"`
	} `json:"serials"`
	Inject []struct {
		Label   string `json:"label"`
		Payload string `json:"payload"`
		Serial  int64  `json:"serial"`
		Hex     string `json:"hex"`
	} `json:"inject"`
}

func TestAgainstRecordedCases(t *testing.T) {
	var c escposCases
	load(t, "escpos_cases.json", &c)
	widths := []int{-1, 0, 1, 2, 5, 10, 47, 48, 100}
	for _, x := range c.Texts {
		if got := DisplayWidth(x.S); got != x.Width {
			t.Errorf("width(%q) = %d, recorded %d", x.S, got, x.Width)
		}
		if got := StripCtrl(x.S); got != x.Strip {
			t.Errorf("strip(%q) = %q, recorded %q", x.S, got, x.Strip)
		}
		for i, w := range widths {
			if got := TruncateToWidth(x.S, w); got != x.Trunc[i] {
				t.Errorf("truncate(%q, %d) = %q, recorded %q", x.S, w, got, x.Trunc[i])
			}
		}
		if got := hx(NewBuilder().Text(x.S).Build()); got != x.TextHex {
			t.Errorf("text(%q) = %s, recorded %s", x.S, got, x.TextHex)
		}
	}
	for _, x := range c.Cols {
		b := NewBuilderCols(x.Columns)
		s := func(i int) string { return x.Args[i].(string) }
		switch x.Op {
		case "two":
			b.TwoCol(s(0), s(1))
		case "three":
			b.ThreeCol(s(0), s(1), s(2))
		case "three_w":
			b.ThreeColW(s(0), s(1), s(2), int(x.Args[3].(float64)), int(x.Args[4].(float64)))
		case "hr":
			b.HR()
		case "hr_eq":
			b.HRChar("=")
		}
		if got := hx(b.Build()); got != x.Hex {
			t.Errorf("%s%v @%d cols:\n go %s\n recorded %s", x.Op, x.Args, x.Columns, got, x.Hex)
		}
	}
	for _, x := range c.Misc.Size {
		if got := hx(NewBuilder().Size(int(x[0].(float64)), int(x[1].(float64))).Build()); got != x[2] {
			t.Errorf("size%v = %s", x, got)
		}
	}
	for _, x := range c.Misc.Feed {
		if got := hx(NewBuilder().Feed(int(x[0].(float64))).Build()); got != x[1] {
			t.Errorf("feed%v = %s", x, got)
		}
	}
	for _, x := range c.Misc.Align {
		if got := hx(NewBuilder().Align(int(x[0].(float64))).Build()); got != x[1] {
			t.Errorf("align%v = %s", x, got)
		}
	}
	for _, x := range c.Misc.Bold {
		if got := hx(NewBuilder().Bold(x[0].(bool)).Build()); got != x[1] {
			t.Errorf("bold%v = %s", x, got)
		}
	}
	if got := hx(NewBuilder().Center("居中").Build()); got != c.Misc.Center {
		t.Errorf("center = %s", got)
	}
	if got := hx(NewBuilder().Init().Cut().Kick(false).Kick(true).Build()); got != c.Misc.InitCutKick {
		t.Errorf("init/cut/kick = %s", got)
	}
	for i, n := range []byte{1, 2, 4} {
		if got := hx(StatusQuery(n)); got != c.Misc.StatusQuery[i] {
			t.Errorf("status_query(%d) = %s", n, got)
		}
	}
	for _, x := range c.Serials {
		if got := hx(SerialFooterBytes(x.Serial)); got != x.FooterHex {
			t.Errorf("serial_footer_bytes(%d) = %s, recorded %s", x.Serial, got, x.FooterHex)
		}
	}
	for _, x := range c.Inject {
		p, _ := hex.DecodeString(x.Payload)
		orig := append([]byte(nil), p...)
		if got := hx(InjectSerial(p, x.Serial)); got != x.Hex {
			t.Errorf("inject_serial(%s, %d) = %s, recorded %s", x.Label, x.Serial, got, x.Hex)
		}
		if hx(p) != hx(orig) {
			t.Errorf("inject_serial modified its input")
		}
	}
}
