package report

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

var monthsLong = [...]string{"januari", "februari", "maart", "april", "mei", "juni", "juli", "augustus", "september", "oktober", "november", "december"}
var monthsShort = [...]string{"jan", "feb", "mrt", "apr", "mei", "jun", "jul", "aug", "sep", "okt", "nov", "dec"}
var weekdays = [...]string{"zo", "ma", "di", "wo", "do", "vr", "za"}

// num formats v with dec decimals the Dutch way: 1.234,5.
func num(v float64, dec int) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "–"
	}
	s := strconv.FormatFloat(math.Abs(v), 'f', dec, 64)
	if s == strconv.FormatFloat(0, 'f', dec, 64) {
		v = 0 // no "-0,0"
	}
	intPart, frac, _ := strings.Cut(s, ".")
	var b strings.Builder
	if v < 0 {
		b.WriteString("-")
	}
	for i, c := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(c)
	}
	if frac != "" {
		b.WriteByte(',')
		b.WriteString(frac)
	}
	return b.String()
}

// eur formats an amount: € 1.234,56 or -€ 12,30.
func eur(v float64) string {
	if math.Round(v*100) < 0 {
		return "-€ " + num(-v, 2)
	}
	return "€ " + num(v, 2)
}

// kwh picks the decimals by size.
func qty(v float64, unit string) string {
	dec := 1
	switch {
	case math.Abs(v) >= 1000:
		dec = 0
	case math.Abs(v) < 10:
		dec = 2
	}
	return num(v, dec) + " " + unit
}

// pct formats a change in percent with a sign: +12% / -5%.
func pct(cur, prev float64) (string, bool) {
	if prev == 0 || math.IsNaN(cur) {
		return "", false
	}
	p := (cur - prev) / math.Abs(prev) * 100
	if math.Abs(p) >= 1000 {
		return "", false
	}
	sign := "+"
	if math.Round(p) < 0 {
		sign = ""
	}
	return sign + num(math.Round(p), 0) + "%", true
}

func dateLong(t time.Time) string {
	return fmt.Sprintf("%d %s %d", t.Day(), monthsLong[t.Month()-1], t.Year())
}

func dateShort(t time.Time) string {
	return fmt.Sprintf("%d %s %d", t.Day(), monthsShort[t.Month()-1], t.Year())
}

func dateTime(t time.Time) string {
	return fmt.Sprintf("%s %d %s %s", weekdays[t.Weekday()], t.Day(), monthsShort[t.Month()-1], t.Format("15:04"))
}

// periodText describes [from, to) of local midnights: "oktober 2026",
// "1 t/m 7 november 2026", "2025 t/m 2026".
func periodText(from, to time.Time) string {
	last := to.AddDate(0, 0, -1)
	switch {
	case from.Day() == 1 && to.Day() == 1 && from.Month() == 1 && to.Month() == 1 && to.Year() == from.Year()+1:
		return strconv.Itoa(from.Year())
	case from.Day() == 1 && to.Day() == 1 && from.AddDate(0, 1, 0).Equal(to):
		return monthsLong[from.Month()-1] + " " + strconv.Itoa(from.Year())
	case from.Day() == 1 && to.Day() == 1:
		if from.Year() == last.Year() {
			return monthsLong[from.Month()-1] + " t/m " + monthsLong[last.Month()-1] + " " + strconv.Itoa(from.Year())
		}
		return monthsLong[from.Month()-1] + " " + strconv.Itoa(from.Year()) + " t/m " + monthsLong[last.Month()-1] + " " + strconv.Itoa(last.Year())
	case from.Equal(last):
		return dateLong(from)
	case from.Year() == last.Year() && from.Month() == last.Month():
		return fmt.Sprintf("%d t/m %d %s %d", from.Day(), last.Day(), monthsLong[from.Month()-1], from.Year())
	case from.Year() == last.Year():
		return fmt.Sprintf("%d %s t/m %d %s %d", from.Day(), monthsLong[from.Month()-1], last.Day(), monthsLong[last.Month()-1], from.Year())
	}
	return dateLong(from) + " t/m " + dateLong(last)
}

// bucketLabel labels a bucket on a chart axis.
func bucketLabel(t time.Time, bucket string, multiYear bool) string {
	if bucket == "month" {
		if multiYear {
			return monthsShort[t.Month()-1] + " '" + t.Format("06")
		}
		return monthsShort[t.Month()-1]
	}
	if t.Day() == 1 {
		return "1 " + monthsShort[t.Month()-1]
	}
	return strconv.Itoa(t.Day())
}

// FileName is the name of a report's PDF: rapport-2026-10.pdf,
// rapport-2026-11-02_2026-11-08.pdf.
func FileName(from, to time.Time) string {
	last := to.AddDate(0, 0, -1)
	if from.Day() == 1 && to.Day() == 1 && from.AddDate(0, 1, 0).Equal(to) {
		return "rapport-" + from.Format("2006-01") + ".pdf"
	}
	if from.Equal(last) {
		return "rapport-" + from.Format("2006-01-02") + ".pdf"
	}
	return "rapport-" + from.Format("2006-01-02") + "_" + last.Format("2006-01-02") + ".pdf"
}

// PeriodText is periodText for callers outside the package (mail subjects).
func PeriodText(from, to time.Time) string { return periodText(from, to) }
