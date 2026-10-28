// SPDX-License-Identifier: GPL-3.0-only
// Copyright (c) 2026 Ninon

// Package pyfmt reproduces the Python behaviours the earlier version of this
// kit had, because stored JSON and printed bytes depend on them: float repr, round(), floor division, str(), truthiness, strftime, and
// json.dumps / json.loads with ordered objects.
package pyfmt

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// F is a Python float. Any float the Python code would produce (a `/`
// division, round(x, n), float()) should be an F or float64 in Go; both
// serialize like Python (10.0 stays "10.0", never "10").
type F float64

// Repr is Python repr(float): shortest round-trip digits, ".0" on integral
// values, scientific notation below 1e-4 and from 1e16 up.
func Repr(f float64) string {
	switch {
	case math.IsNaN(f):
		return "nan"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	}
	if f == 0 {
		if math.Signbit(f) {
			return "-0.0"
		}
		return "0.0"
	}
	e := strconv.FormatFloat(f, 'e', -1, 64) // d.ddddde±XX
	mant, expS, _ := strings.Cut(e, "e")
	exp, _ := strconv.Atoi(expS)
	neg := strings.HasPrefix(mant, "-")
	mant = strings.TrimPrefix(mant, "-")
	digits := strings.Replace(mant, ".", "", 1)
	sign := ""
	if neg {
		sign = "-"
	}
	if exp < -4 || exp >= 16 {
		m := digits[:1]
		if len(digits) > 1 {
			m += "." + digits[1:]
		}
		es := "+"
		if exp < 0 {
			es = "-"
			exp = -exp
		}
		return fmt.Sprintf("%s%se%s%02d", sign, m, es, exp)
	}
	var s string
	if exp >= 0 {
		if len(digits) <= exp+1 {
			s = digits + strings.Repeat("0", exp+1-len(digits)) + ".0"
		} else {
			s = digits[:exp+1] + "." + digits[exp+1:]
		}
	} else {
		s = "0." + strings.Repeat("0", -exp-1) + digits
	}
	return sign + s
}

func jsonFloat(f float64, allowNaN bool) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	return Repr(f)
}

// FloorDiv is Python a // b for ints.
func FloorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// Mod is Python a % b for ints (result has the sign of b).
func Mod(a, b int64) int64 {
	m := a % b
	if m != 0 && ((m < 0) != (b < 0)) {
		m += b
	}
	return m
}

// MaxI / MinI replace the builtins Go 1.20 does not have.
func MaxI(a int64, rest ...int64) int64 {
	for _, b := range rest {
		if b > a {
			a = b
		}
	}
	return a
}

// Str is Python str(v) for the value types this codebase passes around.
func Str(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case bool:
		if x {
			return "True"
		}
		return "False"
	case string:
		return x
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case int32:
		return strconv.FormatInt(int64(x), 10)
	case F:
		return Repr(float64(x))
	case float64:
		return Repr(x)
	case *int64:
		if x == nil {
			return "None"
		}
		return strconv.FormatInt(*x, 10)
	case *string:
		if x == nil {
			return "None"
		}
		return *x
	case []byte:
		return string(x)
	}
	return fmt.Sprint(v)
}

// Truthy is Python bool(v).
func Truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case int:
		return x != 0
	case int64:
		return x != 0
	case int32:
		return x != 0
	case F:
		return x != 0
	case float64:
		return x != 0
	case []byte:
		return len(x) > 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	case *OMap:
		return x != nil && x.Len() > 0
	case *int64:
		return x != nil && *x != 0
	case *string:
		return x != nil && *x != ""
	}
	return true
}

// FromMs is datetime.fromtimestamp(ms / 1000, tz=loc).
func FromMs(ms int64, loc *time.Location) time.Time {
	return time.UnixMilli(ms).In(loc)
}

var weekdayAbbr = []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
var weekdayFull = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}
var monthAbbr = []string{"", "Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}

// Strftime is datetime.strftime for the directives this codebase uses.
func Strftime(t time.Time, layout string) string {
	var b strings.Builder
	for i := 0; i < len(layout); i++ {
		c := layout[i]
		if c != '%' || i+1 >= len(layout) {
			b.WriteByte(c)
			continue
		}
		i++
		switch layout[i] {
		case 'Y':
			fmt.Fprintf(&b, "%04d", t.Year())
		case 'y':
			fmt.Fprintf(&b, "%02d", t.Year()%100)
		case 'm':
			fmt.Fprintf(&b, "%02d", int(t.Month()))
		case 'd':
			fmt.Fprintf(&b, "%02d", t.Day())
		case 'H':
			fmt.Fprintf(&b, "%02d", t.Hour())
		case 'I':
			h := t.Hour() % 12
			if h == 0 {
				h = 12
			}
			fmt.Fprintf(&b, "%02d", h)
		case 'M':
			fmt.Fprintf(&b, "%02d", t.Minute())
		case 'S':
			fmt.Fprintf(&b, "%02d", t.Second())
		case 'f':
			fmt.Fprintf(&b, "%06d", t.Nanosecond()/1000)
		case 'p':
			if t.Hour() < 12 {
				b.WriteString("AM")
			} else {
				b.WriteString("PM")
			}
		case 'j':
			fmt.Fprintf(&b, "%03d", t.YearDay())
		case 'w':
			fmt.Fprintf(&b, "%d", int(t.Weekday()))
		case 'a':
			b.WriteString(weekdayAbbr[t.Weekday()])
		case 'A':
			b.WriteString(weekdayFull[t.Weekday()])
		case 'b':
			b.WriteString(monthAbbr[t.Month()])
		case '%':
			b.WriteByte('%')
		default:
			b.WriteByte('%')
			b.WriteByte(layout[i])
		}
	}
	return b.String()
}
