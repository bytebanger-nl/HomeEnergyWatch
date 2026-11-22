package report

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/ista"
)

// unitOf is ista's unit, or a Dutch default.
func unitOf(u string) string {
	if strings.TrimSpace(u) == "" {
		return "eenheden"
	}
	return u
}

// heatingNote says how far ista measured, as text (in the summary) or as a
// warning under the heating chart.
func (d *doc) heatingNote(h *Heating, summary bool) {
	for _, a := range h.Addresses {
		if a.Complete {
			continue
		}
		var line string
		switch {
		case a.LastMeasured.IsZero():
			line = "Ista heeft voor deze periode nog niets gemeten."
		default:
			line = "Ista heeft gemeten tot en met " + dateLong(a.LastMeasured) + "."
		}
		if a.Missing > 0 {
			line += fmt.Sprintf(" %s %s nog niet gemeten en tellen niet mee.", dayCount(a.Missing), isAre(a.Missing))
		}
		line += " De verwarming is daardoor nog niet compleet; de cijfers kunnen nog veranderen."
		if len(h.Addresses) > 1 {
			line = a.Name + ": " + line
		}
		if summary {
			d.text(line)
		} else {
			d.font("", 9)
			d.color(cWarn)
			d.MultiCell(contentW, 4.8, line, "", "L", false)
			d.Ln(1)
		}
	}
}

func dayCount(n int) string {
	if n == 1 {
		return "1 dag"
	}
	return fmt.Sprintf("%d dagen", n)
}

func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "zijn"
}

// istaPeriod names a settlement period as the app does ("2025/26").
func istaPeriod(y int) string { return fmt.Sprintf("%d/%02d", y-1, y%100) }

// priceSource says where the prices of a settlement period come from.
func priceSource(c ista.Costs) string {
	switch c.PriceSource {
	case "settlement":
		return "uit de afrekening"
	case "contract":
		return "uit het contract"
	case "previous":
		s := "uit de afrekening " + istaPeriod(c.PriceFrom)
		if k := c.Contract; k != nil && k.CorrectionPct != 0 {
			s += fmt.Sprintf(", %s%s%%", map[bool]string{true: "+", false: ""}[k.CorrectionPct > 0], num(k.CorrectionPct, 0))
		}
		return s
	}
	return ""
}

// heatingContract names the heating "contract" of an address in the
// report's period, for the summary.
func heatingContract(a HeatingAddress) string {
	var parts []string
	for _, p := range a.Prices {
		c := p.Costs
		s := "ista " + istaPeriod(c.Period.Y)
		switch {
		case c.UnitPrice == nil:
			s += " (geen prijzen)"
		case c.PriceSource == "settlement":
			s += " (afrekening)"
		case c.PriceSource == "contract":
			s += " (contract)"
		case c.PriceSource == "previous":
			s += " (afrekening " + istaPeriod(c.PriceFrom) + ")"
		}
		parts = append(parts, s)
	}
	if len(parts) == 0 {
		return "ista"
	}
	return strings.Join(parts, ", ")
}

// heatingLabels returns the labels of ista buckets (their first day).
func (d *doc) heatingLabel(start string) (string, bool) {
	t, err := time.ParseInLocation("2006-01-02", start, d.r.Loc)
	if err != nil {
		return "", false
	}
	return bucketLabel(t, d.r.Bucket, d.multiYear()), true
}

func pv(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// HeatingFollowsNote says why heating is not in a report sent on time.
const HeatingFollowsNote = "ista heeft nog niet alle dagen van deze periode gemeten; het overzicht verwarming volgt in een aparte mail zodra dat wel zo is (hooguit een week later)."

func (d *doc) heatingDeferred() {
	d.heading("Verwarming", 20)
	d.note(upperFirst(HeatingFollowsNote))
}

func (d *doc) heating() {
	h := d.r.Heating
	d.heading("Verwarming", 70)
	// the radiators are listed under Per apparaat when that is in the report
	roomsElsewhere := d.r.Devices != nil && len(d.r.Devices.Heating) > 0
	for i, a := range h.Addresses {
		if len(h.Addresses) > 1 {
			if i > 0 {
				d.Ln(3)
			}
			d.subheading(a.Name, 60)
		}
		unit := unitOf(a.Unit)
		t := a.Usage.Total
		var list []kpi
		if t.Total != nil {
			k := kpi{label: "Verbruik", value: num(*t.Total, 0) + " " + unit}
			if t.LastYear != nil {
				if c, ok := change(*t.Total, *t.LastYear, "vorig jaar"); ok {
					k.subs = append(k.subs, c)
				}
			}
			list = append(list, k)
			if days := t.Days - t.Missing; days > 0 {
				list = append(list, kpi{label: "Gemiddeld per dag", value: num(*t.Total/float64(days), 1) + " " + unit})
			}
		}
		if t.Building != nil {
			list = append(list, kpi{label: "Gebouwgemiddelde", value: num(*t.Building, 0) + " " + unit, subs: []sub{{text: "zelfde dagen", color: cText2}}})
		}
		c := a.Costs
		if c != nil && c.Total.Total != nil {
			list = append(list, kpi{label: "Kosten", value: eur(*c.Total.Total), subs: []sub{
				{text: "variabel " + eur(pv(c.Total.Variable)), color: cText2}, {text: "vast " + eur(pv(c.Total.Fixed)), color: cText2}}})
		}
		if a.Complete {
			list = append(list, kpi{label: "Gemeten door ista", value: "compleet", subs: []sub{{text: "t/m " + dateShort(a.LastMeasured), color: cText2}}})
		} else {
			k := kpi{label: "Gemeten door ista", value: "nog niet compleet"}
			if !a.LastMeasured.IsZero() {
				k.subs = append(k.subs, sub{text: "t/m " + dateShort(a.LastMeasured), color: cWarn})
			}
			list = append(list, k)
		}
		d.tiles(list)
		if !a.Complete {
			d.heatingNote(&Heating{Addresses: []HeatingAddress{a}}, false)
		}

		// usage per day or month: only what ista measured
		var labels []string
		meas := make([]float64, 0, len(a.Usage.Buckets))
		for _, b := range a.Usage.Buckets {
			l, ok := d.heatingLabel(b.Start)
			if !ok {
				continue
			}
			labels = append(labels, l)
			meas = append(meas, pv(b.Total))
		}
		if len(labels) > 1 {
			d.subheading("Per "+d.bucketWord()+" ("+unit+")", 58)
			d.barChart(barChart{labels: labels, height: 58, unit: unit,
				series: []series{{"Gemeten", cHeat, meas}}})
		}

		// costs per day or month, as for electricity and gas
		if c != nil && len(c.Buckets) > 1 {
			var cl []string
			n := len(c.Buckets)
			variable, fixed := make([]float64, 0, n), make([]float64, 0, n)
			for _, b := range c.Buckets {
				l, ok := d.heatingLabel(b.Start)
				if !ok {
					continue
				}
				cl = append(cl, l)
				variable, fixed = append(variable, pv(b.Variable)), append(fixed, pv(b.Fixed))
			}
			ss := []series{{"Verwarming", cHeat, variable}, {"Vaste kosten", cFixed, fixed}}
			d.subheading("Kosten per "+d.bucketWord()+" (€)", 58)
			d.barChart(barChart{labels: cl, height: 58, unit: "€", series: ss})
		}

		if c != nil && c.Total.Total != nil {
			d.subheading("Opbouw", 30)
			rows := [][]string{
				{"Verwarming: verbruik", num(pv(c.Total.Units), 0) + " " + unit, eur(pv(c.Total.Variable))},
				{"Variabele kosten", "", eur(pv(c.Total.Variable))},
				{"Vaste kosten (vloeroppervlak)", "", eur(pv(c.Total.Fixed))},
				{"Totaal", "", eur(*c.Total.Total)},
			}
			d.table([]col{{"", 0.5, "L"}, {"Hoeveelheid", 0.25, "R"}, {"Bedrag", 0.25, "R"}}, rows, map[int]bool{1: true, 3: true})
			d.note("Variabele kosten hangen af van je verbruik: de eenheden van je radiatoren maal de prijs per eenheid. Vaste kosten (vaste energiekosten, overige kosten en de diensten van ista, verdeeld naar vloeroppervlak) betaal je per dag, wat je ook stookt.")
			if c.Total.Unpriced > 0 {
				d.note(fmt.Sprintf("Voor %s zijn er nog geen prijzen; die dagen tellen niet mee in de kosten.", dayCount(c.Total.Unpriced)))
			}
		}
		d.heatingPrices(a)

		// per room, unless Per apparaat lists the radiators
		if !roomsElsewhere && len(a.Rooms) > 0 && t.Total != nil && *t.Total > 0 {
			d.subheading("Per kamer", 30)
			d.radiators(a, false)
		}
		d.note("Volgens mijn.ista.nl: de eenheden van de radiatormeters, al met hun factor omgerekend.")
	}
}

// heatingPrices lists the prices of each settlement period in the report,
// like the contract line under the energy costs.
func (d *doc) heatingPrices(a HeatingAddress) {
	for _, p := range a.Prices {
		c := p.Costs
		line := "Contract: ista, afrekenperiode " + istaPeriod(c.Period.Y) + " (" + dateLong(p.Start) + " t/m " + dateLong(p.End) + ")"
		if c.UnitPrice == nil || c.AreaPrice == nil {
			d.note(line + ": nog geen prijzen. Voeg in de app een ista-contract toe met de prijzen van je afrekening, of voer de afrekening van een eerdere periode in.")
			continue
		}
		line += fmt.Sprintf(": € %s per eenheid en € %s per dm²", num(*c.UnitPrice, 6), num(*c.AreaPrice, 6))
		if src := priceSource(c); src != "" {
			line += " (" + src + ")"
		}
		if c.AreaDM2 > 0 {
			line += ", vloeroppervlak " + num(c.AreaDM2, 0) + " dm²"
		}
		if c.Final {
			line += ". De afrekening is ingevoerd: deze periode is definitief"
		}
		d.note(line + ".")
	}
}

// radiators draws the table of an address's radiators: units, share and
// the variable costs per radiator, then the fixed part and the total.
func (d *doc) radiators(a HeatingAddress, title bool) {
	unit := unitOf(a.Unit)
	t := a.Usage.Total
	total := pv(t.Total)
	var roomCost []*float64
	if a.Costs != nil {
		roomCost = a.Costs.Total.Rooms
	}
	type row struct {
		name  string
		units *float64
		cost  *float64
	}
	var list []row
	for j, room := range a.Rooms {
		name := room.Name
		if name == "" {
			name = fmt.Sprintf("Radiator %d", room.RadNr)
		}
		r := row{name: name}
		if j < len(t.Units) {
			r.units = t.Units[j]
		}
		if j < len(roomCost) {
			r.cost = roomCost[j]
		}
		list = append(list, r)
	}
	sort.SliceStable(list, func(i, j int) bool { return pv(list[i].units) > pv(list[j].units) })
	var rows [][]string
	for _, r := range list {
		val, share, cost := "–", "", ""
		if r.units != nil {
			val = num(*r.units, 0) + " " + unit
			if total > 0 {
				share = num(*r.units/total*100, 1) + "%"
			}
		}
		if r.cost != nil {
			cost = eur(*r.cost)
		}
		rows = append(rows, []string{r.name, val, share, cost})
	}
	bold := map[int]bool{}
	if c := a.Costs; c != nil && c.Total.Total != nil {
		rows = append(rows, []string{"Vaste kosten (vloeroppervlak)", "", "", eur(pv(c.Total.Fixed))})
		rows = append(rows, []string{"Totaal verwarming", num(total, 0) + " " + unit, "100%", eur(*c.Total.Total)})
	} else {
		rows = append(rows, []string{"Totaal verwarming", num(total, 0) + " " + unit, "100%", ""})
	}
	bold[len(rows)-1] = true
	first := "Kamer"
	if title {
		first = "Radiator"
	}
	cols := []col{{first, 0.45, "L"}, {"Verbruik", 0.2, "R"}, {"Aandeel", 0.15, "R"}, {"Kosten", 0.2, "R"}}
	if a.Costs == nil {
		cols = cols[:3] // no prices: no costs column
	}
	d.table(cols, rows, bold)
	if a.Costs != nil {
		d.note("Kosten per radiator: de eenheden van die radiator maal de prijs per eenheid van de afrekenperiode. De vaste kosten (naar vloeroppervlak) horen bij de woning, niet bij een radiator; ze staan daarom apart, net zoals de vaste kosten van stroom en gas niet over de apparaten worden verdeeld.")
	}
}

// heatingAdvance shows where ista's running settlement period stands, in
// the same form as the advance of electricity, gas and water.
func (d *doc) heatingAdvance(list []HeatingAdvance) {
	for _, ha := range list {
		c := ha.Costs
		title := "Verwarming · ista"
		if len(list) > 1 {
			title += " · " + ha.Name
		}
		start, end := parseDay(c.Period.Start), parseDay(c.Period.End)
		pairs := [][2]string{{"Afrekenperiode", dateLong(start) + " t/m " + dateLong(end)}}
		hasAdv := c.Contract != nil && len(c.Contract.Advance) > 0
		if hasAdv {
			pairs = append(pairs,
				[2]string{"Voorschot per maand", eur(c.Monthly)},
				[2]string{fmt.Sprintf("Betaald tot nu (%d termijnen)", c.Payments), eur(c.Paid)})
		}
		if c.CostSoFar != nil {
			pairs = append(pairs, [2]string{"Kosten tot nu", eur(*c.CostSoFar)})
			if hasAdv {
				pairs = append(pairs, [2]string{"Saldo", eur(c.Paid - *c.CostSoFar)})
			}
		}
		switch {
		case c.CostExpected != nil:
			pairs = append(pairs, [2]string{"Verwachte kosten hele periode", eur(*c.CostExpected)})
			if c.VariableExpected != nil && c.FixedExpected != nil {
				pairs = append(pairs,
					[2]string{"   waarvan variabel (verbruik)", eur(*c.VariableExpected)},
					[2]string{"   waarvan vaste kosten", eur(*c.FixedExpected)})
			}
			if hasAdv {
				all := c.Paid + c.ToPay
				result := all - *c.CostExpected
				res := "terug: " + eur(result)
				if result < 0 {
					res = "bijbetalen: " + eur(-result)
				}
				pairs = append(pairs, [2]string{"Verwachte eindafrekening", res})
				if c.CostLow != nil && c.CostHigh != nil && *c.CostHigh-*c.CostLow >= 1 {
					pairs = append(pairs, [2]string{"Bandbreedte", eur(all-*c.CostHigh) + " tot " + eur(all-*c.CostLow)})
				}
				if c.Advice != nil {
					pairs = append(pairs, [2]string{"Advies voorschot per maand", eur(*c.Advice)})
				}
			}
		case c.UnitPrice == nil:
			pairs = append(pairs, [2]string{"Prognose", "nog geen prijzen voor deze periode"})
		case c.ForecastError != "":
			pairs = append(pairs, [2]string{"Prognose", "niet mogelijk (zie hieronder)"})
		default:
			pairs = append(pairs, [2]string{"Prognose", "vul bij het ista-contract een verwacht jaarverbruik in"})
		}
		d.subheading(title, float64(len(pairs))*5.4+2)
		d.keyValues(pairs)
		unit := unitOf(c.Unit)
		var method string
		if !c.Final && c.UnitsRest != nil {
			rest := num(*c.UnitsRest, 0) + " " + unit
			switch c.RestBasis {
			case "last_year":
				method = "Prognose voor de rest van de periode: " + rest + ", uit je verbruik van dezelfde dagen van de vorige periode."
			case "estimate":
				how := "een standaard stookprofiel"
				if c.Profile == "building" {
					how = "het stookpatroon van het gebouw"
				}
				method = "Prognose voor de rest van de periode: " + rest + ", uit je verwachte jaarverbruik (zoals ingevuld bij het contract) verdeeld met " + how + "."
			}
		}
		if !c.Final && c.CostExpected == nil && c.ForecastError != "" {
			method = c.ForecastError
		}
		if !c.Final {
			if p := percentagesText(c.Percentages); p != "" {
				method = strings.TrimSpace(method + " " + p)
			}
		}
		if !hasAdv {
			method = strings.TrimSpace(method + " Er is geen voorschot ingevuld bij het ista-contract; daarom alleen de kosten.")
		}
		if method != "" {
			d.note(method)
		}
	}
}

// heatingTotals sums the heating costs of every address: variable, fixed,
// total; ok is false when no address has costs.
func heatingTotals(h *Heating) (variable, fixed, total float64, ok bool) {
	if h == nil {
		return 0, 0, 0, false
	}
	for _, a := range h.Addresses {
		if c := a.Costs; c != nil && c.Total.Total != nil {
			variable += pv(c.Total.Variable)
			fixed += pv(c.Total.Fixed)
			total += *c.Total.Total
			ok = true
		}
	}
	return variable, fixed, math.Round(total*100) / 100, ok
}
