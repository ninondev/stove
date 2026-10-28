// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

package pyfmt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// OMap is an insertion-ordered JSON object (a Python dict). Use it wherever
// the Python code json.dumps()s a dict into the database or onto paper, so
// the stored text keeps Python's key order. Plain map[string]any is fine for
// HTTP responses (key order is not part of the contract there).
type OMap struct {
	keys []string
	vals map[string]any
}

// NewOMap builds an OMap from alternating key, value arguments.
func NewOMap(kv ...any) *OMap {
	m := &OMap{vals: map[string]any{}}
	for i := 0; i+1 < len(kv); i += 2 {
		m.Set(kv[i].(string), kv[i+1])
	}
	return m
}

func (m *OMap) Set(k string, v any) {
	if m.vals == nil {
		m.vals = map[string]any{}
	}
	if _, ok := m.vals[k]; !ok {
		m.keys = append(m.keys, k)
	}
	m.vals[k] = v
}

func (m *OMap) Get(k string) (any, bool) {
	if m == nil {
		return nil, false
	}
	v, ok := m.vals[k]
	return v, ok
}

func (m *OMap) Keys() []string {
	if m == nil {
		return nil
	}
	return append([]string(nil), m.keys...)
}

func (m *OMap) Len() int {
	if m == nil {
		return 0
	}
	return len(m.keys)
}

// DumpOpts selects json.dumps arguments.
type DumpOpts struct {
	EnsureASCII bool // Python's default is True
	Compact     bool // separators=(",", ":"); default ", " and ": "
	SortKeys    bool
}

// Dumps serializes v the way Python's json module does.
func Dumps(v any, o DumpOpts) string {
	var b strings.Builder
	enc := encoder{o: o, b: &b}
	enc.value(v)
	return b.String()
}

type encoder struct {
	o DumpOpts
	b *strings.Builder
}

func (e *encoder) itemSep() string {
	if e.o.Compact {
		return ","
	}
	return ", "
}

func (e *encoder) keySep() string {
	if e.o.Compact {
		return ":"
	}
	return ": "
}

func (e *encoder) str(s string) {
	b := e.b
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			switch {
			case r < 0x20:
				fmt.Fprintf(b, `\u%04x`, r)
			case e.o.EnsureASCII && r > 0x7e:
				if r > 0xffff {
					r1, r2 := utf16.EncodeRune(r)
					fmt.Fprintf(b, `\u%04x\u%04x`, r1, r2)
				} else {
					fmt.Fprintf(b, `\u%04x`, r)
				}
			default:
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}

func (e *encoder) value(v any) {
	b := e.b
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
		return
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
		return
	case string:
		e.str(x)
		return
	case int:
		b.WriteString(strconv.Itoa(x))
		return
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
		return
	case int32:
		b.WriteString(strconv.FormatInt(int64(x), 10))
		return
	case F:
		b.WriteString(jsonFloat(float64(x), true))
		return
	case float64:
		b.WriteString(jsonFloat(x, true))
		return
	case json.Number:
		b.WriteString(x.String())
		return
	case *OMap:
		if x == nil {
			b.WriteString("null")
			return
		}
		keys := x.keys
		if e.o.SortKeys {
			keys = append([]string(nil), keys...)
			sort.Strings(keys)
		}
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteString(e.itemSep())
			}
			e.str(k)
			b.WriteString(e.keySep())
			e.value(x.vals[k])
		}
		b.WriteByte('}')
		return
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteString(e.itemSep())
			}
			e.str(k)
			b.WriteString(e.keySep())
			e.value(x[k])
		}
		b.WriteByte('}')
		return
	case []any:
		b.WriteByte('[')
		for i, it := range x {
			if i > 0 {
				b.WriteString(e.itemSep())
			}
			e.value(it)
		}
		b.WriteByte(']')
		return
	case json.Marshaler:
		raw, err := x.MarshalJSON()
		if err != nil {
			panic(err)
		}
		b.Write(raw)
		return
	}
	e.reflectValue(reflect.ValueOf(v))
}

func (e *encoder) reflectValue(rv reflect.Value) {
	b := e.b
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface:
		if rv.IsNil() {
			b.WriteString("null")
			return
		}
		e.value(rv.Elem().Interface())
	case reflect.Slice, reflect.Array:
		if rv.Kind() == reflect.Slice && rv.IsNil() {
			b.WriteString("[]") // Python lists are never null; ported code builds them
			return
		}
		b.WriteByte('[')
		for i := 0; i < rv.Len(); i++ {
			if i > 0 {
				b.WriteString(e.itemSep())
			}
			e.value(rv.Index(i).Interface())
		}
		b.WriteByte(']')
	case reflect.Map:
		keys := rv.MapKeys()
		ks := make([]string, len(keys))
		idx := map[string]reflect.Value{}
		for i, k := range keys {
			var s string
			switch k.Kind() {
			case reflect.String:
				s = k.String()
			case reflect.Int, reflect.Int64, reflect.Int32:
				s = strconv.FormatInt(k.Int(), 10)
			default:
				s = fmt.Sprint(k.Interface())
			}
			ks[i] = s
			idx[s] = k
		}
		sort.Strings(ks)
		b.WriteByte('{')
		for i, s := range ks {
			if i > 0 {
				b.WriteString(e.itemSep())
			}
			e.str(s)
			b.WriteString(e.keySep())
			e.value(rv.MapIndex(idx[s]).Interface())
		}
		b.WriteByte('}')
	case reflect.Struct:
		t := rv.Type()
		b.WriteByte('{')
		n := 0
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name := f.Name
			omitEmpty := false
			if tag, ok := f.Tag.Lookup("json"); ok {
				if tag == "-" {
					continue
				}
				parts := strings.Split(tag, ",")
				if parts[0] != "" {
					name = parts[0]
				}
				for _, p := range parts[1:] {
					if p == "omitempty" {
						omitEmpty = true
					}
				}
			}
			fv := rv.Field(i)
			if omitEmpty && fv.IsZero() {
				continue
			}
			if n > 0 {
				b.WriteString(e.itemSep())
			}
			n++
			e.str(name)
			b.WriteString(e.keySep())
			e.value(fv.Interface())
		}
		b.WriteByte('}')
	case reflect.Bool:
		e.value(rv.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		e.value(rv.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		b.WriteString(strconv.FormatUint(rv.Uint(), 10))
	case reflect.Float32, reflect.Float64:
		e.value(rv.Float())
	case reflect.String:
		e.str(rv.String())
	default:
		panic(fmt.Errorf("pyfmt: cannot encode %s", rv.Type()))
	}
}

// ValidUTF8 reports whether b is valid UTF-8 (used when decoding bodies).
func ValidUTF8(b []byte) bool { return utf8.Valid(b) }

// Compact returns compact JSON for HTTP and WebSocket replies
// (FastAPI: separators=(",", ":"), ensure_ascii=False).
func Compact(v any) []byte {
	var buf bytes.Buffer
	buf.WriteString(Dumps(v, DumpOpts{Compact: true}))
	return buf.Bytes()
}
