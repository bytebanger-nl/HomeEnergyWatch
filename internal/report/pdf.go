package report

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"codeberg.org/go-pdf/fpdf"

	"github.com/bytebanger-nl/homeenergywatch/internal/costs"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// colors of the PWA's light theme (app.css), so a printed report looks
// like the app
type rgb struct{ r, g, b int }

var (
	cText    = rgb{11, 11, 11}
	cText2   = rgb{82, 81, 78}
	cMuted   = rgb{137, 135, 129}
	cGrid    = rgb{225, 224, 217}
	cAxis    = rgb{195, 194, 183}
	cSurface = rgb{240, 239, 236}
	cImport  = rgb{42, 120, 214}
	cExport  = rgb{235, 104, 52}
	cGas     = rgb{27, 175, 122}
	cFixed   = rgb{139, 111, 208}
	cWater   = rgb{14, 143, 168}
	cHeat    = rgb{216, 90, 48}
	cGood    = rgb{12, 163, 12}
	cBad     = rgb{208, 59, 59}
	cWarn    = rgb{176, 120, 0}
)

const (
	// the PDF core font (no font file to embed): text is written in
	// Windows-1252, see cp1252
	font     = "Helvetica"
	marginX  = 15.0
	marginT  = 20.0
	marginB  = 16.0
	pageW    = 210.0
	pageH    = 297.0
	contentW = pageW - 2*marginX
)

// doc wraps fpdf with the report's building blocks.
type doc struct {
	*fpdf.Fpdf
	r *Report
}

func (d *doc) color(c rgb)     { d.SetTextColor(c.r, c.g, c.b) }
func (d *doc) fill(c rgb)      { d.SetFillColor(c.r, c.g, c.b) }
func (d *doc) draw(c rgb)      { d.SetDrawColor(c.r, c.g, c.b) }
func (d *doc) bottom() float64 { return pageH - marginB }

// need starts a new page when less than h mm is left.
func (d *doc) need(h float64) {
	if d.GetY()+h > d.bottom() {
		d.AddPage()
	}
}

func (d *doc) font(style string, size float64) { d.SetFont(font, style, size) }

// The text methods of fpdf, with the text converted to the core font's
// encoding.

func (d *doc) CellFormat(w, h float64, txt, border string, ln int, align string, fill bool, link int, linkStr string) {
	d.Fpdf.CellFormat(w, h, cp1252(txt), border, ln, align, fill, link, linkStr)
}

func (d *doc) MultiCell(w, h float64, txt, border, align string, fill bool) {
	d.Fpdf.MultiCell(w, h, cp1252(txt), border, align, fill)
}

func (d *doc) Text(x, y float64, txt string) { d.Fpdf.Text(x, y, cp1252(txt)) }

func (d *doc) GetStringWidth(s string) float64 { return d.Fpdf.GetStringWidth(cp1252(s)) }

// cp1252 encodes s in Windows-1252, the encoding of the PDF core fonts;
// characters it lacks become "?".
func cp1252(s string) string {
	b := make([]byte, 0, len(s))
	for _, r := range s {
		switch {
		case r < 0x80 || (r >= 0xA0 && r <= 0xFF):
			b = append(b, byte(r))
		case r == '−': // minus sign
			b = append(b, '-')
		default:
			if c, ok := cp1252High[r]; ok {
				b = append(b, c)
			} else {
				b = append(b, '?')
			}
		}
	}
	return string(b)
}

var cp1252High = map[rune]byte{
	'€': 0x80, '‚': 0x82, 'ƒ': 0x83, '„': 0x84, '…': 0x85, '†': 0x86, '‡': 0x87, 'ˆ': 0x88,
	'‰': 0x89, 'Š': 0x8A, '‹': 0x8B, 'Œ': 0x8C, 'Ž': 0x8E, '‘': 0x91, '’': 0x92, '“': 0x93,
	'”': 0x94, '•': 0x95, '–': 0x96, '—': 0x97, '˜': 0x98, '™': 0x99, 'š': 0x9A, '›': 0x9B,
	'œ': 0x9C, 'ž': 0x9E, 'Ÿ': 0x9F,
}

// PDF renders the report.
func (r *Report) PDF(w io.Writer) error {
	f := fpdf.New("P", "mm", "A4", "")
	f.SetMargins(marginX, marginT, marginX)
	f.SetAutoPageBreak(true, marginB)
	f.SetTitle(r.Title+" – "+periodText(r.From, r.To), true)
	f.SetCreator("Home Energy Watch "+r.Version, true)
	f.SetCreationDate(r.Generated)
	f.SetModificationDate(r.Generated)
	f.AliasNbPages("{nb}")
	d := &doc{Fpdf: f, r: r}
	f.SetHeaderFunc(func() {
		if f.PageNo() == 1 {
			return
		}
		d.font("", 8)
		d.color(cMuted)
		f.SetXY(marginX, 8)
		d.CellFormat(contentW/2, 5, r.Title, "", 0, "L", false, 0, "")
		d.CellFormat(contentW/2, 5, periodText(r.From, r.To), "", 0, "R", false, 0, "")
		d.draw(cGrid)
		f.SetLineWidth(0.2)
		f.Line(marginX, 14, pageW-marginX, 14)
		f.SetY(marginT)
	})
	f.SetFooterFunc(func() {
		d.font("", 7.5)
		d.color(cMuted)
		f.SetXY(marginX, pageH-11)
		d.CellFormat(contentW*0.7, 5, "Home Energy Watch · gemaakt op "+dateLong(r.Generated)+" om "+r.Generated.Format("15:04"), "", 0, "L", false, 0, "")
		d.CellFormat(contentW*0.3, 5, fmt.Sprintf("Pagina %d van {nb}", f.PageNo()), "", 0, "R", false, 0, "")
	})
	f.AddPage()
	d.cover()
	empty := true
	for _, part := range []struct {
		ok bool
		fn func()
	}{
		{r.Summary != nil, d.summary},
		{r.Electricity != nil, d.electricity},
		{r.Gas != nil, d.gas},
		{r.Costs != nil, d.costs},
		{r.Water != nil, d.water},
		{r.Heating != nil, d.heating},
		{r.HeatingDeferred, d.heatingDeferred},
		{r.Devices != nil, d.devices},
		{r.Scenarios != nil, d.scenarios},
		{r.Grid != nil, d.grid},
		{r.Advance != nil, d.advance},
	} {
		if part.ok {
			part.fn()
			empty = false
		}
	}
	if empty {
		d.note("Er is voor deze periode niets om te tonen: geen metingen van de gekozen onderdelen.")
	}
	return f.Output(w)
}

// Bytes renders the report into memory.
func (r *Report) Bytes() ([]byte, error) {
	var buf bytes.Buffer
	if err := r.PDF(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (d *doc) cover() {
	r := d.r
	d.SetY(marginT - 4)
	d.font("B", 22)
	d.color(cText)
	d.MultiCell(contentW, 10, r.Title, "", "L", false)
	d.font("", 13)
	d.color(cText2)
	d.MultiCell(contentW, 7, periodText(r.From, r.To), "", "L", false)
	d.font("", 9)
	d.color(cMuted)
	line := fmt.Sprintf("%d dagen", r.Days())
	if r.Days() == 1 {
		line = "1 dag"
	}
	if r.Meter != "" {
		line += " · " + r.Meter
	}
	d.MultiCell(contentW, 5, line, "", "L", false)
	d.Ln(4)
}

// heading starts a section; keepWith is the room (mm) the section's first
// block needs on the same page.
func (d *doc) heading(title string, keepWith float64) {
	if d.GetY() > marginT+1 {
		d.Ln(5)
	}
	d.need(14 + keepWith)
	d.font("B", 14)
	d.color(cText)
	d.CellFormat(contentW, 8, title, "", 1, "L", false, 0, "")
	d.draw(cGrid)
	d.SetLineWidth(0.3)
	y := d.GetY()
	d.Line(marginX, y, pageW-marginX, y)
	d.SetY(y + 3)
}

func (d *doc) subheading(title string, keepWith float64) {
	d.Ln(2)
	d.need(8 + keepWith)
	d.font("B", 10.5)
	d.color(cText)
	d.CellFormat(contentW, 6, title, "", 1, "L", false, 0, "")
	d.Ln(1)
}

func (d *doc) text(s string) {
	d.font("", 9.5)
	d.color(cText)
	d.MultiCell(contentW, 5, s, "", "L", false)
	d.Ln(1)
}

func (d *doc) note(s string) {
	d.font("", 8.5)
	d.color(cMuted)
	d.MultiCell(contentW, 4.4, s, "", "L", false)
	d.Ln(1)
}

// kpi is one tile with a big value.
type kpi struct {
	label, value string
	subs         []sub
}

type sub struct {
	text  string
	color rgb
	arrow int // 1: a triangle up before the text, -1: down
}

// tiles draws up to four tiles per row.
func (d *doc) tiles(list []kpi) {
	if len(list) == 0 {
		return
	}
	const gap, h = 3.0, 23.0
	per := min(4, len(list))
	if len(list) == 5 || len(list) == 6 {
		per = 3
	}
	// fewer per row when the longest text would not fit in a tile
	need := 0.0
	for _, k := range list {
		d.font("B", 14)
		need = math.Max(need, d.GetStringWidth(k.value)+6)
		d.font("", 7.5)
		for _, s := range k.subs {
			extra := 6.0
			if s.arrow != 0 {
				extra = 9.5
			}
			need = math.Max(need, d.GetStringWidth(s.text)+extra)
		}
	}
	for per > 1 && (contentW-gap*float64(per-1))/float64(per) < need {
		per--
	}
	w := (contentW - gap*float64(per-1)) / float64(per)
	for i := 0; i < len(list); i += per {
		d.need(h + gap)
		y := d.GetY()
		for j := i; j < min(i+per, len(list)); j++ {
			k := list[j]
			x := marginX + float64(j-i)*(w+gap)
			d.fill(cSurface)
			d.RoundedRect(x, y, w, h, 2, "1234", "F")
			d.SetXY(x+3, y+2.5)
			d.font("", 8)
			d.color(cText2)
			d.CellFormat(w-6, 4, k.label, "", 2, "L", false, 0, "")
			d.SetX(x + 3)
			d.font("B", 14)
			d.color(cText)
			d.CellFormat(w-6, 7.5, k.value, "", 2, "L", false, 0, "")
			for _, s := range k.subs {
				d.SetX(x + 3)
				if s.arrow != 0 {
					d.triangle(x+4.1, d.GetY()+1.9, s.arrow)
					d.SetX(x + 6.2)
				}
				d.font("", 7.5)
				d.color(s.color)
				d.CellFormat(w-9, 3.8, s.text, "", 2, "L", false, 0, "")
			}
		}
		d.SetY(y + h + gap)
	}
}

// triangle draws a small arrow head centred on (x, y): up (dir 1) or down.
func (d *doc) triangle(x, y float64, dir int) {
	const s = 1.1
	d.fill(cText2)
	dy := s * float64(dir)
	d.Polygon([]fpdf.PointType{{X: x - s, Y: y + dy}, {X: x + s, Y: y + dy}, {X: x, Y: y - dy}}, "F")
}

// change describes cur against prev ("12% meer dan vorige periode", with a
// triangle).
func change(cur, prev float64, label string) (sub, bool) {
	p, ok := pct(cur, prev)
	if !ok {
		return sub{}, false
	}
	switch {
	case p == "+0%" || p == "0%":
		return sub{text: "gelijk aan " + label, color: cText2}, true
	case strings.HasPrefix(p, "-"):
		return sub{text: strings.TrimPrefix(p, "-") + " minder dan " + label, color: cText2, arrow: -1}, true
	}
	return sub{text: strings.TrimPrefix(p, "+") + " meer dan " + label, color: cText2, arrow: 1}, true
}

// col is a table column.
type col struct {
	title string
	w     float64 // share of the content width
	align string
}

// table draws a table; rows marked in bold (index) are drawn bold with a
// rule above them.
func (d *doc) table(cols []col, rows [][]string, bold map[int]bool) {
	const rh = 5.6
	sum := 0.0
	for _, c := range cols {
		sum += c.w
	}
	header := func() {
		d.font("B", 8)
		d.color(cText2)
		for _, c := range cols {
			d.CellFormat(contentW*c.w/sum, rh, c.title, "", 0, c.align, false, 0, "")
		}
		d.Ln(rh)
		d.draw(cAxis)
		d.SetLineWidth(0.2)
		d.Line(marginX, d.GetY(), pageW-marginX, d.GetY())
	}
	d.need(rh * float64(min(len(rows), 3)+1))
	header()
	for i, row := range rows {
		if d.GetY()+rh > d.bottom() {
			d.AddPage()
			header()
		}
		if bold[i] {
			d.draw(cAxis)
			d.Line(marginX, d.GetY(), pageW-marginX, d.GetY())
			d.font("B", 9)
		} else {
			d.font("", 9)
		}
		d.color(cText)
		for j, c := range cols {
			txt := ""
			if j < len(row) {
				txt = row[j]
			}
			d.CellFormat(contentW*c.w/sum, rh, fit(d, txt, contentW*c.w/sum-1), "", 0, c.align, false, 0, "")
		}
		d.Ln(rh)
		if !bold[i] && i < len(rows)-1 && !bold[i+1] {
			d.draw(cGrid)
			d.SetLineWidth(0.1)
			d.Line(marginX, d.GetY(), pageW-marginX, d.GetY())
		}
	}
	d.Ln(2)
}

// fit shortens s with "…" until it fits in w mm.
func fit(d *doc, s string, w float64) string {
	if d.GetStringWidth(s) <= w {
		return s
	}
	rs := []rune(s)
	for len(rs) > 1 && d.GetStringWidth(string(rs)+"…") > w {
		rs = rs[:len(rs)-1]
	}
	return string(rs) + "…"
}

// keyValues draws label/value pairs in two columns.
func (d *doc) keyValues(pairs [][2]string) {
	const rh = 5.4
	for _, p := range pairs {
		d.need(rh)
		d.font("", 9)
		d.color(cText2)
		d.CellFormat(contentW*0.45, rh, p[0], "", 0, "L", false, 0, "")
		d.color(cText)
		d.font("B", 9)
		d.CellFormat(contentW*0.55, rh, p[1], "", 1, "L", false, 0, "")
	}
	d.Ln(1)
}

func (d *doc) multiYear() bool { return d.r.From.Year() != d.r.To.AddDate(0, 0, -1).Year() }

func (d *doc) labels(bs []costs.Bucket) []string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = bucketLabel(b.Start.In(d.r.Loc), d.r.Bucket, d.multiYear())
	}
	return out
}

func (d *doc) bucketWord() string {
	if d.r.Bucket == costs.BucketMonth {
		return "maand"
	}
	return "dag"
}

// ---- sections ----

func (d *doc) summary() {
	s := d.r.Summary
	d.heading("Samenvatting", 30)
	var list []kpi
	add := func(label string, cur float64, unit string, prev, ly *costs.Bucket, get func(*costs.Bucket) float64, money bool) {
		k := kpi{label: label, value: qty(cur, unit)}
		if money {
			k.value = eur(cur)
		}
		if prev != nil {
			if c, ok := change(cur, get(prev), "vorige periode"); ok {
				k.subs = append(k.subs, c)
			}
		}
		if ly != nil {
			if c, ok := change(cur, get(ly), "vorig jaar"); ok {
				k.subs = append(k.subs, c)
			}
		}
		list = append(list, k)
	}
	if e := s.Energy; e != nil {
		imp := func(b *costs.Bucket) float64 { return b.ImportKWh }
		add("Stroom afgenomen", e.ImportKWh, "kWh", s.EnergyPrev, s.EnergyLastYear, imp, false)
		if e.ExportKWh > 0 || (s.EnergyLastYear != nil && s.EnergyLastYear.ExportKWh > 0) {
			add("Stroom teruggeleverd", e.ExportKWh, "kWh", s.EnergyPrev, s.EnergyLastYear, func(b *costs.Bucket) float64 { return b.ExportKWh }, false)
		}
		if e.GasM3 > 0 || (s.EnergyLastYear != nil && s.EnergyLastYear.GasM3 > 0) {
			add("Gas", e.GasM3, "m³", s.EnergyPrev, s.EnergyLastYear, func(b *costs.Bucket) float64 { return b.GasM3 }, false)
		}
		if e.Missing.ContractDays < d.r.Days() {
			add("Kosten stroom en gas", e.TotalEUR, "", s.EnergyPrev, s.EnergyLastYear, func(b *costs.Bucket) float64 { return b.TotalEUR }, true)
		}
	}
	if w := s.Water; w != nil {
		add("Water", w.WaterM3, "m³", s.WaterPrev, s.WaterLastYear, func(b *costs.Bucket) float64 { return b.WaterM3 }, false)
		if w.Missing.ContractDays < d.r.Days() {
			add("Kosten water", w.TotalEUR, "", s.WaterPrev, s.WaterLastYear, func(b *costs.Bucket) float64 { return b.TotalEUR }, true)
		}
	}
	if h := s.Heating; h != nil {
		for _, a := range h.Addresses {
			label := "Verwarming"
			if len(h.Addresses) > 1 {
				label += " · " + a.Name
			}
			t := a.Usage.Total
			if t.Total != nil {
				k := kpi{label: label, value: num(*t.Total, 0) + " " + unitOf(a.Unit)}
				if t.LastYear != nil {
					if c, ok := change(*t.Total, *t.LastYear, "vorig jaar"); ok {
						k.subs = append(k.subs, c)
					}
				}
				if !a.Complete {
					k.subs = append(k.subs, sub{text: "ista nog niet compleet", color: cWarn})
				}
				list = append(list, k)
			}
			if c := a.Costs; c != nil && c.Total.Total != nil {
				k := kpi{label: "Kosten verwarming", value: eur(*c.Total.Total)}
				if !a.Complete {
					k.subs = append(k.subs, sub{text: "t/m laatst gemeten dag", color: cWarn})
				}
				list = append(list, k)
			}
		}
	}
	rows, total, parts, incomplete := d.costParts()
	if parts > 1 {
		list = append(list, kpi{label: "Totale kosten", value: eur(total), subs: []sub{{text: "alle onderdelen samen", color: cText2}}})
	}
	d.tiles(list)
	if len(rows) > 0 {
		d.subheading("Kosten per onderdeel", 30)
		bold := map[int]bool{}
		if parts > 1 {
			bold[len(rows)-1] = true
		}
		d.table([]col{{"Onderdeel", 0.17, "L"}, {"Contract", 0.41, "L"}, {"Variabel", 0.14, "R"}, {"Vast", 0.14, "R"}, {"Totaal", 0.14, "R"}}, rows, bold)
		if incomplete {
			d.note("* Nog niet compleet (zie Volledigheid): ontbrekende metingen of prijzen, of bij verwarming dagen die ista nog niet gemeten heeft (die tellen niet mee).")
		}
	}

	var cmp []string
	if s.EnergyPrev != nil || s.WaterPrev != nil {
		cmp = append(cmp, "vorige periode: "+periodText(s.PrevFrom, s.PrevTo))
	}
	if s.EnergyLastYear != nil || s.WaterLastYear != nil {
		cmp = append(cmp, "vorig jaar: "+periodText(s.LastYearFrom, s.LastYearTo))
	}
	if len(cmp) > 0 {
		d.note("Vergeleken met " + strings.Join(cmp, "; ") + ". De kosten zijn incl. btw, met de contracten van die periode.")
	}

	if s.Expected > 0 || (s.Heating != nil && !s.Heating.Complete()) {
		d.subheading("Volledigheid", 12)
	}
	if h := s.Heating; h != nil && !h.Complete() {
		d.heatingNote(h, true)
	}
	if s.Expected > 0 {
		good := s.Expected - s.Missing - s.Partial - s.Anomalous
		share := math.Max(0, float64(good)) / float64(s.Expected) * 100
		line := fmt.Sprintf("%s%% van de kwartieren van de P1-meter is volledig gemeten.", num(math.Floor(share*10)/10, 1))
		var parts []string
		if s.Missing > 0 {
			parts = append(parts, quarters(s.Missing)+" ontbreken")
		}
		if s.Partial > 0 {
			parts = append(parts, quarters(s.Partial)+" zijn deels geschat")
		}
		if s.Anomalous > 0 {
			parts = append(parts, quarters(s.Anomalous)+" hebben een meterreset")
		}
		if len(parts) > 0 {
			line += " " + upperFirst(joinAnd(parts)) + "."
		}
		if s.Imported > 0 {
			line += " " + upperFirst(quarters(s.Imported)) + " komen uit een import van de HomeWizard-app."
		}
		if s.PriceMissing > 0 {
			line += fmt.Sprintf(" Bij %d kwartieren ontbreekt een prijs.", s.PriceMissing)
		}
		if s.ContractDays > 0 {
			line += fmt.Sprintf(" %d dagen vallen buiten een contract en zijn niet geprijsd.", s.ContractDays)
		}
		d.text(line)
		if len(s.Gaps) > 0 {
			var rows [][]string
			for i, g := range s.Gaps {
				if i == 8 {
					rows = append(rows, []string{fmt.Sprintf("en nog %d", len(s.Gaps)-8), "", ""})
					break
				}
				to := g.To.In(d.r.Loc)
				rows = append(rows, []string{gapLabel(g.Kind), dateTime(g.From.In(d.r.Loc)) + " – " + dateTime(to), gapCount(g)})
			}
			d.table([]col{{"Wat", 0.3, "L"}, {"Wanneer", 0.5, "L"}, {"Omvang", 0.2, "R"}}, rows, nil)
		}
	}
}

// costParts returns the rows of the summary's cost table (one per part
// with costs, then the total when there is more than one), the total and
// the number of parts.
func (d *doc) costParts() ([][]string, float64, int, bool) {
	s := d.r.Summary
	var rows [][]string
	total, parts := 0.0, 0
	names := func(l []string) string {
		if len(l) == 0 {
			return "–"
		}
		return strings.Join(l, ", ")
	}
	var sv, sf float64
	addRow := func(part, contract string, variable, fixed, sum float64, complete bool) {
		t := eur(sum)
		if !complete {
			t += " *"
		}
		rows = append(rows, []string{part, contract, eur(variable), eur(fixed), t})
		total += sum
		sv += variable
		sf += fixed
		parts++
	}
	incomplete := false
	if e := s.Energy; e != nil && e.Missing.ContractDays < d.r.Days() {
		addRow("Stroom en gas", names(s.EnergyContracts), e.TotalEUR-e.FixedEUR, e.FixedEUR, e.TotalEUR, e.Complete)
		incomplete = incomplete || !e.Complete
	}
	if w := s.Water; w != nil && w.Missing.ContractDays < d.r.Days() {
		addRow("Water", names(s.WaterContracts), w.TotalEUR-w.FixedEUR, w.FixedEUR, w.TotalEUR, w.Complete)
		incomplete = incomplete || !w.Complete
	}
	if h := s.Heating; h != nil {
		for _, a := range h.Addresses {
			c := a.Costs
			if c == nil || c.Total.Total == nil {
				continue
			}
			part := "Verwarming"
			if len(h.Addresses) > 1 {
				part += " · " + a.Name
			}
			done := a.Complete && c.Total.Unpriced == 0 && c.Total.Missing == 0
			addRow(part, heatingContract(a), pv(c.Total.Variable), pv(c.Total.Fixed), *c.Total.Total, done)
			incomplete = incomplete || !done
		}
	}
	if parts > 1 {
		t := eur(total)
		if incomplete {
			t += " *"
		}
		rows = append(rows, []string{"Totaal", "", eur(sv), eur(sf), t})
	}
	return rows, total, parts, incomplete
}

func quarters(n int) string {
	if n == 1 {
		return "1 kwartier"
	}
	return num(float64(n), 0) + " kwartieren"
}

func joinAnd(parts []string) string {
	if len(parts) < 2 {
		return strings.Join(parts, "")
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " en " + parts[len(parts)-1]
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func gapLabel(kind string) string {
	switch kind {
	case costs.GapQuarters:
		return "Geen meting"
	case costs.GapPartial:
		return "Deels geschat"
	case costs.GapAnomalous:
		return "Meterreset"
	case costs.GapPrices:
		return "Geen stroomprijs"
	case costs.GapContract:
		return "Geen contract"
	}
	return "Geen gasprijs"
}

func gapCount(g costs.Gap) string {
	if g.Kind == costs.GapContract {
		if g.Count == 1 {
			return "1 dag"
		}
		return fmt.Sprintf("%d dagen", g.Count)
	}
	if g.Count == 1 {
		return "1 kwartier"
	}
	return fmt.Sprintf("%d kwartieren", g.Count)
}

func (d *doc) electricity() {
	e := d.r.Electricity
	d.heading("Stroom", 70)
	t := e.Total
	d.tiles([]kpi{
		{label: "Afgenomen", value: qty(t.ImportKWh, "kWh")},
		{label: "Teruggeleverd", value: qty(t.ExportKWh, "kWh")},
		{label: "Netto van het net", value: qty(t.ImportKWh-t.ExportKWh, "kWh")},
		{label: "Gemiddeld per dag", value: qty(t.ImportKWh/float64(max(1, d.r.Days())), "kWh"), subs: []sub{{text: "afgenomen", color: cText2}}},
	})
	imp := make([]float64, len(e.Buckets))
	exp := make([]float64, len(e.Buckets))
	for i, b := range e.Buckets {
		imp[i], exp[i] = b.ImportKWh, -b.ExportKWh
	}
	d.subheading("Per "+d.bucketWord()+" (kWh)", 58)
	d.barChart(barChart{
		labels: d.labels(e.Buckets), height: 58, unit: "kWh",
		series: []series{{"Afgenomen", cImport, imp}, {"Teruggeleverd", cExport, exp}},
	})

	if e.WeekdayDays+e.WeekendDays > 0 {
		d.subheading("Gemiddeld dagprofiel (kW, afname min teruglevering)", 58)
		var ls []lineSeries
		if e.WeekdayDays > 0 {
			ls = append(ls, lineSeries{fmt.Sprintf("Werkdagen (%d)", e.WeekdayDays), cImport, e.Weekday[:]})
		}
		if e.WeekendDays > 0 {
			ls = append(ls, lineSeries{fmt.Sprintf("Weekend (%d)", e.WeekendDays), cFixed, e.Weekend[:]})
		}
		d.lineChart(58, "kW", ls)
		d.note("Per kwartier van de dag het gemiddelde vermogen over de periode. Onder nul is teruglevering (zon).")
	}

	if e.BaseLoadNights > 0 {
		d.subheading("Sluipverbruik", 16)
		line := fmt.Sprintf("Het laagste verbruik 's nachts (00:00–05:00) is gemiddeld %s W (de mediaan over %d nachten). Dat is %s per jaar",
			num(e.BaseLoadW, 0), e.BaseLoadNights, qty(e.BaseLoadYearKWh, "kWh"))
		if e.BaseLoadYearEUR > 0 {
			line += fmt.Sprintf(", ongeveer %s bij de gemiddelde stroomprijs van deze periode", eur(e.BaseLoadYearEUR))
		}
		d.text(line + ".")
	}
	if len(e.Peaks) > 0 {
		d.subheading("Hoogste kwartieren", 30)
		var rows [][]string
		for _, p := range e.Peaks {
			rows = append(rows, []string{dateTime(p.Start.In(d.r.Loc)), num(p.KW, 2) + " kW"})
		}
		d.table([]col{{"Kwartier", 0.6, "L"}, {"Gemiddeld vermogen", 0.4, "R"}}, rows, nil)
	}
}

func (d *doc) gas() {
	g := d.r.Gas
	d.heading("Gas", 70)
	list := []kpi{{label: "Verbruik", value: qty(g.TotalM3, "m³")},
		{label: "Gemiddeld per dag", value: qty(g.TotalM3/float64(max(1, d.r.Days())), "m³")}}
	if g.HasLastYear {
		k := kpi{label: "Zelfde periode vorig jaar", value: qty(g.LastYearM3, "m³")}
		if c, ok := change(g.TotalM3, g.LastYearM3, "vorig jaar"); ok {
			k.subs = append(k.subs, sub{text: "nu " + c.text, color: cText2, arrow: c.arrow})
		}
		list = append(list, k)
	}
	if g.HighestBucket >= 0 && len(g.Buckets) > 1 {
		b := g.Buckets[g.HighestBucket]
		label := "Hoogste dag"
		when := dateShort(b.Start.In(d.r.Loc))
		if d.r.Bucket == costs.BucketMonth {
			label = "Hoogste maand"
			when = monthsLong[b.Start.In(d.r.Loc).Month()-1]
		}
		list = append(list, kpi{label: label, value: qty(b.GasM3, "m³"), subs: []sub{{text: when, color: cText2}}})
	}
	d.tiles(list)
	vals := make([]float64, len(g.Buckets))
	for i, b := range g.Buckets {
		vals[i] = b.GasM3
	}
	d.subheading("Per "+d.bucketWord()+" (m³)", 58)
	d.barChart(barChart{labels: d.labels(g.Buckets), height: 58, unit: "m³", series: []series{{"Gas", cGas, vals}}})
	d.note("Een gasdag loopt van 06:00 tot 06:00; de meter geeft gas per uur of per kwartier door, dus een dag kan iets van de nacht ervoor of erna bevatten.")
}

func (d *doc) costs() {
	c := d.r.Costs
	t := c.Total
	d.heading("Kosten", 70)
	perDay := t.TotalEUR / float64(max(1, d.r.Days()))
	list := []kpi{{label: "Totaal (incl. btw)", value: eur(t.TotalEUR), subs: []sub{
		{text: "variabel " + eur(t.TotalEUR-t.FixedEUR), color: cText2}, {text: "vast " + eur(t.FixedEUR), color: cText2}}},
		{label: "Gemiddeld per dag", value: eur(perDay)}}
	if t.ImportKWh > 0 && t.ElectricityEUR+t.ElectricityTaxEUR > 0 {
		list = append(list, kpi{label: "Stroom per kWh", value: "€ " + num((t.ElectricityEUR+t.ElectricityTaxEUR)/t.ImportKWh, 3), subs: []sub{{text: "levering + belasting", color: cText2}}})
	}
	if t.GasM3 > 0 && t.GasEUR+t.GasTaxEUR > 0 {
		list = append(list, kpi{label: "Gas per m³", value: "€ " + num((t.GasEUR+t.GasTaxEUR)/t.GasM3, 3), subs: []sub{{text: "levering + belasting", color: cText2}}})
	}
	d.tiles(list)
	if !t.Complete {
		d.note("Niet alles kon worden berekend (zie Volledigheid in de samenvatting); de bedragen zijn dan te laag.")
	}

	power := make([]float64, len(c.Buckets))
	gas := make([]float64, len(c.Buckets))
	fixed := make([]float64, len(c.Buckets))
	credit := make([]float64, len(c.Buckets))
	hasGas := false
	for i, b := range c.Buckets {
		power[i] = b.ElectricityEUR + b.ElectricityTaxEUR
		gas[i] = b.GasEUR + b.GasTaxEUR
		fixed[i] = b.FixedEUR
		credit[i] = -(b.ExportEUR + b.NettingEUR)
		hasGas = hasGas || gas[i] != 0
	}
	ss := []series{{"Stroom", cImport, power}}
	if hasGas {
		ss = append(ss, series{"Gas", cGas, gas})
	}
	ss = append(ss, series{"Vaste kosten", cFixed, fixed}, series{"Teruglevering en saldering", cExport, credit})
	d.subheading("Per "+d.bucketWord()+" (€)", 58)
	d.barChart(barChart{labels: d.labels(c.Buckets), height: 58, unit: "€", series: ss})

	d.subheading("Opbouw", 50)
	rows := [][]string{
		{"Stroom: levering", qty(t.ImportKWh, "kWh"), eur(t.ElectricityEUR)},
		{"Stroom: energiebelasting", "", eur(t.ElectricityTaxEUR)},
	}
	if t.ExportEUR != 0 || t.ExportKWh > 0 {
		rows = append(rows, []string{"Teruglevering", qty(t.ExportKWh, "kWh"), eur(-t.ExportEUR)})
	}
	if t.NettingEUR != 0 {
		rows = append(rows, []string{"Saldering", "", eur(-t.NettingEUR)})
	}
	if hasGas || t.GasM3 > 0 {
		rows = append(rows, []string{"Gas: levering", qty(t.GasM3, "m³"), eur(t.GasEUR)}, []string{"Gas: energiebelasting", "", eur(t.GasTaxEUR)})
	}
	rows = append(rows, []string{"Variabele kosten", "", eur(t.TotalEUR - t.FixedEUR)})
	variable := len(rows) - 1
	rows = append(rows, []string{"Vaste kosten", "", eur(t.FixedEUR)}, []string{"Totaal", "", eur(t.TotalEUR)})
	d.table([]col{{"", 0.5, "L"}, {"Hoeveelheid", 0.25, "R"}, {"Bedrag", 0.25, "R"}}, rows, map[int]bool{variable: true, len(rows) - 2: true, len(rows) - 1: true})
	d.note("Variabele kosten hangen af van je verbruik: levering en energiebelasting, min teruglevering en saldering. Vaste kosten (vastrecht, netbeheer, vermindering energiebelasting) betaal je per dag, wat je ook verbruikt.")
	if len(c.ContractNames) > 0 {
		d.note("Contract: " + strings.Join(c.ContractNames, ", ") + ". Alle bedragen zijn incl. btw.")
	}

	if dy := c.Dynamic; dy != nil {
		d.subheading("Dynamisch contract: wanneer verbruikte je?", 20)
		diff := dy.WeightedCt - dy.AverageCt
		how := "even duur als"
		switch {
		case diff < -0.05:
			how = "goedkoper dan"
		case diff > 0.05:
			how = "duurder dan"
		}
		line := fmt.Sprintf("Je stroom kostte op de markt gemiddeld %s ct/kWh (gewogen naar je verbruik), tegen %s ct/kWh gemiddeld over alle kwartieren. Je verbruik viel dus %s gemiddeld",
			num(dy.WeightedCt, 2), num(dy.AverageCt, 2), how)
		if how != "even duur als" {
			line += fmt.Sprintf(" (%s ct/kWh, over %s ongeveer %s excl. btw)", num(math.Abs(diff), 2), qty(dy.ImportKWh, "kWh"), eur(math.Abs(diff)*dy.ImportKWh/100))
		}
		d.text(line + ".")
		d.note("Kale marktprijzen (day-ahead, excl. opslag, belasting en btw). Wie verbruik naar goedkope uren schuift, komt onder het gemiddelde uit.")
	}
	if c.Cheapest != nil && c.Dearest != nil {
		d.subheading("Goedkoopste en duurste dag", 14)
		d.keyValues([][2]string{
			{"Goedkoopste dag: " + dateLong(c.Cheapest.Start.In(d.r.Loc)), eur(c.Cheapest.TotalEUR) + " (" + qty(c.Cheapest.ImportKWh, "kWh") + ")"},
			{"Duurste dag: " + dateLong(c.Dearest.Start.In(d.r.Loc)), eur(c.Dearest.TotalEUR) + " (" + qty(c.Dearest.ImportKWh, "kWh") + ")"},
		})
	}
}

func (d *doc) devices() {
	v := d.r.Devices
	d.heading("Per apparaat", 40)
	if len(v.List) > 0 {
		if len(v.Heating) > 0 {
			d.subheading("Stroom", 30)
		}
		d.powerDevices()
	}
	for _, a := range v.Heating {
		title := "Verwarming (ista)"
		if len(v.Heating) > 1 {
			title += " · " + a.Name
		}
		d.subheading(title, 30)
		d.radiators(a, true)
	}
}

func (d *doc) powerDevices() {
	v := d.r.Devices
	var rows [][]string
	for _, u := range v.List {
		share := ""
		if v.ConnectionKWh > 0 {
			share = num(u.ImportKWh/v.ConnectionKWh*100, 1) + "%"
		}
		cost := eur(u.CostEUR)
		if !u.Complete {
			cost += " *"
		}
		rows = append(rows, []string{u.Name, qty(u.ImportKWh, "kWh"), share, cost})
	}
	if v.ConnectionKWh > 0 {
		rows = append(rows, []string{"Overig (niet apart gemeten)", qty(v.OtherKWh, "kWh"), num(v.OtherKWh/v.ConnectionKWh*100, 1) + "%", ""})
		rows = append(rows, []string{"Hele aansluiting (P1-meter)", qty(v.ConnectionKWh, "kWh"), "100%", ""})
	}
	bold := map[int]bool{}
	if v.ConnectionKWh > 0 {
		bold[len(rows)-1] = true
	}
	d.table([]col{{"Apparaat", 0.45, "L"}, {"Verbruik", 0.2, "R"}, {"Aandeel", 0.15, "R"}, {"Kosten", 0.2, "R"}}, rows, bold)
	note := "Kosten per apparaat: het verbruik geprijsd met het contract van dat moment (bij dynamisch de marktprijs van dat kwartier), incl. belasting en btw, zonder vaste kosten, teruglevering en saldering."
	if v.Incomplete {
		note += " * Niet alles kon worden berekend."
	}
	d.note(note)
}

func partLabel(part string, water bool) string {
	switch {
	case water:
		return "Water"
	case part == costs.PartElectricity:
		return "Stroom"
	case part == costs.PartGas:
		return "Gas"
	}
	return "Stroom en gas"
}

func (d *doc) advance() {
	a := d.r.Advance
	d.heading("Voorschot en prognose", 50)
	d.note("De stand van nu (" + dateLong(d.r.Generated) + "), los van de periode van dit rapport.")
	show := func(st *costs.AdvanceStatus, water bool) {
		pairs := [][2]string{
			{"Afrekenperiode", dateLong(st.From.In(d.r.Loc)) + " t/m " + dateLong(st.To.In(d.r.Loc).AddDate(0, 0, -1))},
			{"Voorschot per maand", eur(st.MonthlyEUR)},
			{fmt.Sprintf("Betaald tot nu (%d termijnen)", st.Payments), eur(st.PaidEUR)},
			{"Kosten tot nu", eur(st.CostEUR)},
			{"Saldo", eur(st.BalanceEUR)},
		}
		if f := st.Forecast; f != nil {
			res := "terug: " + eur(f.ResultEUR)
			if f.ResultEUR < 0 {
				res = "bijbetalen: " + eur(-f.ResultEUR)
			}
			pairs = append(pairs,
				[2]string{"Verwachte kosten hele periode", eur(f.TotalCostEUR)},
				[2]string{"   waarvan variabel (verbruik)", eur(f.UsageCostEUR)},
				[2]string{"   waarvan vaste kosten", eur(f.FixedCostEUR)},
				[2]string{"Verwachte eindafrekening", res},
				[2]string{"Bandbreedte", eur(f.LowEUR) + " tot " + eur(f.HighEUR)})
			if f.AdviceMonthlyEUR != nil {
				pairs = append(pairs, [2]string{"Advies voorschot per maand", eur(*f.AdviceMonthlyEUR)})
			}
		} else if st.ForecastError != "" {
			pairs = append(pairs, [2]string{"Prognose", "niet mogelijk (zie hieronder)"})
		}
		d.subheading(partLabel(st.Part, water)+" · "+st.ContractName, float64(len(pairs))*5.4+2)
		d.keyValues(pairs)
		if st.Forecast == nil && st.ForecastError != "" {
			d.note(st.ForecastError)
		} else if f := st.Forecast; f != nil && st.Now.Before(st.To) {
			if p := percentagesText(f.Method.Percentages); p != "" {
				d.note(p)
			}
		}
	}
	for _, st := range a.Energy {
		show(st, false)
	}
	for _, st := range a.Water {
		show(st, true)
	}
	d.heatingAdvance(a.Heating)
	d.note("Bij de eindafrekening: positief = geld terug, negatief = bijbetalen. De bandbreedte gaat uit van een ander verbruik en, bij een dynamisch contract, andere prijzen.")
}

func (d *doc) scenarios() {
	s := d.r.Scenarios
	d.heading("Wat-als", 30)
	rows := [][]string{{"Je eigen contract(en)", eur(s.ActualEUR), ""}}
	for _, sc := range s.List {
		diff := sc.TotalEUR - s.ActualEUR
		txt := eur(diff)
		if diff > 0 {
			txt = "+" + txt
		}
		name := sc.Name
		if !sc.Complete {
			name += " *"
		}
		rows = append(rows, []string{name, eur(sc.TotalEUR), txt})
	}
	d.table([]col{{"Contract", 0.5, "L"}, {"Kosten", 0.25, "R"}, {"Verschil", 0.25, "R"}}, rows, map[int]bool{0: true})
	d.note("Dezelfde kwartieren en regels, met de tarieven van elk scenario over dezelfde dagen. Een negatief verschil betekent dat het scenario goedkoper was.")
}

func (d *doc) water() {
	w := d.r.Water
	d.heading("Water", 70)
	list := []kpi{
		{label: "Verbruik", value: qty(w.Total.WaterM3, "m³")},
		{label: "Gemiddeld per dag", value: num(w.Total.WaterM3*1000/float64(max(1, d.r.Days())), 0) + " liter"},
	}
	if w.Priced {
		list = append(list, kpi{label: "Kosten (incl. btw)", value: eur(w.Total.TotalEUR), subs: []sub{
			{text: "variabel " + eur(w.Total.TotalEUR-w.Total.FixedEUR), color: cText2}, {text: "vast " + eur(w.Total.FixedEUR), color: cText2}}})
	}
	list = append(list, kpi{label: "Dit jaar tot nu", value: qty(w.YearM3, "m³"), subs: []sub{{text: "belasting over max. " + num(w.CapM3, 0) + " m³", color: cText2}}})
	d.tiles(list)
	vals := make([]float64, len(w.Buckets))
	scale, unit := 1000.0, "liter" // litres per day, m³ per month
	if d.r.Bucket == costs.BucketMonth {
		scale, unit = 1, "m³"
	}
	for i, b := range w.Buckets {
		vals[i] = b.WaterM3 * scale
	}
	d.subheading("Per "+d.bucketWord()+" ("+unit+")", 58)
	d.barChart(barChart{labels: d.labels(w.Buckets), height: 58, unit: unit, series: []series{{"Water", cWater, vals}}})
	if w.Priced {
		t := w.Total
		if len(w.Buckets) > 1 {
			variable := make([]float64, len(w.Buckets))
			fixed := make([]float64, len(w.Buckets))
			for i, b := range w.Buckets {
				variable[i], fixed[i] = b.TotalEUR-b.FixedEUR, b.FixedEUR
			}
			d.subheading("Kosten per "+d.bucketWord()+" (€)", 58)
			d.barChart(barChart{labels: d.labels(w.Buckets), height: 58, unit: "€",
				series: []series{{"Water", cWater, variable}, {"Vaste kosten", cFixed, fixed}}})
		}
		d.subheading("Opbouw", 40)
		rows := [][]string{
			{"Water: levering", qty(t.WaterM3, "m³"), eur(t.WaterEUR)},
			{"Belasting op leidingwater", "", eur(t.WaterTaxEUR)},
			{"Variabele kosten", "", eur(t.TotalEUR - t.FixedEUR)},
			{"Vaste kosten", "", eur(t.FixedEUR)},
			{"Totaal", "", eur(t.TotalEUR)},
		}
		d.table([]col{{"", 0.5, "L"}, {"Hoeveelheid", 0.25, "R"}, {"Bedrag", 0.25, "R"}}, rows, map[int]bool{2: true, 4: true})
		d.note("Variabele kosten hangen af van je verbruik: de prijs per m³ en de belasting op leidingwater (over max. " + num(w.CapM3, 0) + " m³ per jaar). Vaste kosten (vastrecht) betaal je per dag, wat je ook verbruikt.")
		if !t.Complete {
			d.note("Niet alles kon worden berekend (zie Volledigheid in de samenvatting); de bedragen zijn dan te laag.")
		}
		if len(w.Contracts) > 0 {
			d.note("Contract: " + strings.Join(w.Contracts, ", ") + ". Alle bedragen zijn incl. btw.")
		}
	}
	if w.Nights > 0 {
		d.subheading("Nachtverbruik", 14)
		line := fmt.Sprintf("In %d van de %d nachten met metingen werd tussen 00:00 en 05:00 elk uur water gebruikt.", w.NightsNoZero, w.Nights)
		if w.NightsNoZero > 0 && w.NightsNoZero*2 >= w.Nights {
			d.text(line + " Als er 's nachts echt niemand water gebruikt, kan dat op een lek wijzen (een lopende kraan, toilet of de cv-bijvuller).")
		} else {
			d.text(line + " Er is geen teken van een lek.")
		}
	}
}

func gridKind(kind string) string {
	switch kind {
	case store.GridSag:
		return "Spanningsdip"
	case store.GridSwell:
		return "Spanningspiek"
	case store.GridFail:
		return "Stroomuitval"
	case store.GridLongFail:
		return "Lange stroomuitval"
	}
	return kind
}

func (d *doc) grid() {
	g := d.r.Grid
	d.heading("Netkwaliteit", 30)
	if g.Phases > 0 {
		var rows [][]string
		for i := range g.Phases {
			lo, hi := "–", "–"
			if g.VMin[i] != nil {
				lo = num(*g.VMin[i], 1) + " V  (" + dateTime(g.VMinAt[i].In(d.r.Loc)) + ")"
			}
			if g.VMax[i] != nil {
				hi = num(*g.VMax[i], 1) + " V  (" + dateTime(g.VMaxAt[i].In(d.r.Loc)) + ")"
			}
			rows = append(rows, []string{fmt.Sprintf("L%d", i+1), lo, hi})
		}
		d.table([]col{{"Fase", 0.12, "L"}, {"Laagste spanning", 0.44, "L"}, {"Hoogste spanning", 0.44, "L"}}, rows, nil)
		note := "De norm is 230 V ± 10% (207–253 V)."
		if g.MinutesFrom.After(d.r.From.Add(time.Hour)) {
			note += " Spanning per minuut wordt maar een beperkte tijd bewaard: deze waarden gaan over " + dateLong(g.MinutesFrom.In(d.r.Loc)) + " en later."
		}
		d.note(note)
	}
	if g.EventsTotal == 0 {
		d.text("Geen spanningsdips, -pieken of stroomuitval gemeld door de meter.")
		return
	}
	var parts []string
	for _, k := range []string{store.GridFail, store.GridLongFail, store.GridSag, store.GridSwell} {
		if n := g.Counts[k]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s: %d", gridKind(k), n))
		}
	}
	d.text("De meter meldde: " + strings.Join(parts, ", ") + ".")
	var rows [][]string
	for _, e := range g.Events {
		phase := ""
		if e.Phase > 0 {
			phase = fmt.Sprintf("L%d", e.Phase)
		}
		rows = append(rows, []string{gridKind(e.Kind), phase, fmt.Sprint(e.Count), dateTime(e.Since.In(d.r.Loc)) + " – " + dateTime(e.At.In(d.r.Loc))})
	}
	d.table([]col{{"Soort", 0.25, "L"}, {"Fase", 0.08, "L"}, {"Aantal", 0.1, "R"}, {"Ergens tussen", 0.57, "L"}}, rows, nil)
	if g.EventsTotal > len(g.Events) {
		d.note(fmt.Sprintf("De laatste %d van %d meldingen.", len(g.Events), g.EventsTotal))
	}
	d.note("De meter telt deze gebeurtenissen maar zegt niet wanneer: het moment ligt tussen twee uitlezingen.")
}
