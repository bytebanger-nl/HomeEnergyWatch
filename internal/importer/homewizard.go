// Package importer reads exports of the HomeWizard Energy app and turns them
// into quarter-hour records.
//
// Supported exports (15-minute resolution, local time Europe/Amsterdam):
//
//	P1 electricity: time,Import T1 kWh,Import T2 kWh,Export T1 kWh,Export T2 kWh,L1 max W,L2 max W,L3 max W
//	P1 gas:         time,Total gas used
//	Watermeter:     time,water usage dl
//	Socket/kWh:     time,Import kWh,Export kWh
//
// A socket or kWh-meter export does not say which device it came from; the
// caller matches it by its counter values (LastImportKWh).
//
// Each row holds cumulative meter readings at that moment. Two adjacent rows
// exactly 15 minutes apart give the energy of the quarter starting at the
// first row. Nothing is interpolated across gaps. Rules follow the import
// contract of Grip op Energie (HOMEWIZARD_P1_CSV_CONTRACT v1), extended with
// gas and water.
package importer

import (
	"bufio"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// Kind of export.
type Kind string

const (
	KindElectricity Kind = "electricity"
	KindGas         Kind = "gas"
	KindWater       Kind = "water"
	// KindMeter: one electricity counter, from an Energy Socket or kWh meter.
	KindMeter Kind = "meter"
)

// DeviceKinds returns the gateway device kinds an export can belong to.
func (k Kind) DeviceKinds() []string {
	switch k {
	case KindWater:
		return []string{"water"}
	case KindMeter:
		return []string{"socket", "kwh"}
	}
	return []string{"p1"}
}

// Result describes a parsed export.
type Result struct {
	Kind      Kind            `json:"kind"`
	Rows      int             `json:"rows"`
	FirstData *time.Time      `json:"first_data,omitempty"` // first row with readings
	LastData  *time.Time      `json:"last_data,omitempty"`
	Quarters  []store.Quarter `json:"-"`
	// Counts of quarters per outcome.
	Valid     int  `json:"valid"`
	Gaps      int  `json:"gaps"`       // intervals not exactly 15 minutes apart, or missing readings
	Resets    int  `json:"resets"`     // counter went down
	Glitches  int  `json:"glitches"`   // readings of 0 between real readings (treated as missing)
	BadTimes  int  `json:"bad_times"`  // rows with a time that does not exist (spring DST)
	Ambiguous int  `json:"ambiguous"`  // autumn DST rows that could not be resolved
	EmptyRows int  `json:"empty_rows"` // rows without any reading
	Phases    int  `json:"phases"`     // number of phases with data (electricity)
	HasExport bool `json:"has_export"` // any feed-in in the file
	// LastImportKWh is the last import counter value in the file (sockets
	// and kWh meters), to find the device it came from.
	LastImportKWh *float64 `json:"last_import_kwh,omitempty"`
	// Unattributed is how much each counter advanced during gaps.
	Unattributed map[string]float64 `json:"unattributed,omitempty"`
}

type columns struct {
	kind                   Kind
	time                   int
	imp1, imp2, exp1, exp2 int
	gas, water             int
	phases                 [3]int
}

func detect(header []string) (columns, error) {
	idx := map[string]int{}
	for i, h := range header {
		idx[strings.TrimSpace(strings.TrimPrefix(h, "\uFEFF"))] = i
	}
	get := func(name string) int {
		if i, ok := idx[name]; ok {
			return i
		}
		return -1
	}
	c := columns{time: get("time"), imp1: -1, imp2: -1, exp1: -1, exp2: -1, gas: -1, water: -1,
		phases: [3]int{get("L1 max W"), get("L2 max W"), get("L3 max W")}}
	if c.time < 0 {
		return c, errors.New("kolom 'time' ontbreekt; dit lijkt geen HomeWizard-export")
	}
	switch {
	case get("Import T1 kWh") >= 0:
		c.kind = KindElectricity
		c.imp1, c.imp2, c.exp1, c.exp2 = get("Import T1 kWh"), get("Import T2 kWh"), get("Export T1 kWh"), get("Export T2 kWh")
		if c.imp2 < 0 || c.exp1 < 0 || c.exp2 < 0 {
			return c, errors.New("stroomexport mist een van de kolommen Import T2 kWh, Export T1 kWh of Export T2 kWh")
		}
	case get("Import kWh") >= 0:
		c.kind, c.imp1, c.exp1 = KindMeter, get("Import kWh"), get("Export kWh")
	case get("Total gas used") >= 0:
		c.kind, c.gas = KindGas, get("Total gas used")
	case get("water usage dl") >= 0:
		c.kind, c.water = KindWater, get("water usage dl")
	default:
		return c, fmt.Errorf("onbekend exportformaat (kolommen: %s)", strings.Join(header, ", "))
	}
	return c, nil
}

// reading is one row: counter values at a moment (UTC).
type reading struct {
	at       time.Time
	imp, exp *float64 // kWh, T1+T2
	// impLow and expLow are the T1 (low, "dal") counters of a P1 export
	impLow, expLow *float64
	gas            *float64 // m³
	water          *float64 // m³
}

// Parse reads an export. loc is the time zone of the export (Europe/Amsterdam).
func Parse(r io.Reader, deviceID string, loc *time.Location) (*Result, error) {
	cr := csv.NewReader(bufio.NewReader(r))
	cr.FieldsPerRecord = -1
	cr.ReuseRecord = true
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("bestand is leeg of geen CSV: %w", err)
	}
	cols, err := detect(append([]string(nil), header...))
	if err != nil {
		return nil, err
	}
	res := &Result{Kind: cols.kind}
	var readings []reading
	var prevWall time.Time
	occurrence := 0
	phaseSeen := [3]bool{}
	lastWater := 0.0

	for line := 2; ; line++ {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("regel %d: %w", line, err)
		}
		res.Rows++
		cell := func(i int) string {
			if i < 0 || i >= len(rec) {
				return ""
			}
			return strings.TrimSpace(rec[i])
		}
		num := func(i int) (*float64, error) {
			s := cell(i)
			if s == "" {
				return nil, nil
			}
			v, err := strconv.ParseFloat(s, 64)
			if err != nil || v < 0 {
				return nil, fmt.Errorf("regel %d: ongeldige waarde %q", line, s)
			}
			return &v, nil
		}

		wall, err := time.Parse("2006-01-02 15:04", cell(cols.time))
		if err != nil {
			return nil, fmt.Errorf("regel %d: ongeldige tijd %q", line, cell(cols.time))
		}
		// The export has no UTC offset. In the repeated hour when the clocks
		// go back, a wall time at or before the previous one is the second
		// (winter time) occurrence.
		if !ambiguousWall(wall, loc) {
			occurrence = 0
		} else if !prevWall.IsZero() && !wall.After(prevWall) {
			occurrence = 1
		}
		prevWall = wall
		at, ok, ambiguous := resolve(wall, loc, occurrence)
		if !ok {
			if ambiguous {
				res.Ambiguous++
			} else {
				res.BadTimes++
			}
			readings = append(readings, reading{}) // breaks the chain: no delta across it
			continue
		}

		rd := reading{at: at}
		switch cols.kind {
		case KindElectricity:
			vals := make([]*float64, 4)
			for i, c := range []int{cols.imp1, cols.imp2, cols.exp1, cols.exp2} {
				if vals[i], err = num(c); err != nil {
					return nil, err
				}
			}
			if vals[0] != nil && vals[1] != nil {
				s := *vals[0] + *vals[1]
				rd.imp, rd.impLow = &s, vals[0]
			}
			if vals[2] != nil && vals[3] != nil {
				s := *vals[2] + *vals[3]
				rd.exp, rd.expLow = &s, vals[2]
				if s > 0 {
					res.HasExport = true
				}
			}
			for i, c := range cols.phases {
				if cell(c) != "" {
					phaseSeen[i] = true
				}
			}
		case KindMeter:
			if rd.imp, err = num(cols.imp1); err != nil {
				return nil, err
			}
			if rd.exp, err = num(cols.exp1); err != nil {
				return nil, err
			}
			if rd.exp != nil && *rd.exp > 0 {
				res.HasExport = true
			}
			if rd.imp != nil {
				v := *rd.imp
				res.LastImportKWh = &v
			}
		case KindGas:
			if rd.gas, err = num(cols.gas); err != nil {
				return nil, err
			}
		case KindWater:
			dl, err := num(cols.water)
			if err != nil {
				return nil, err
			}
			if dl != nil {
				// a reading of exactly 0 between real readings is a dropout of
				// the watermeter, not a new meter
				if *dl == 0 && lastWater > 0 {
					res.Glitches++
					dl = nil
				} else {
					lastWater = *dl
					m3 := *dl / 10000 // deciliter -> m³
					rd.water = &m3
				}
			}
		}
		if rd.imp == nil && rd.exp == nil && rd.gas == nil && rd.water == nil {
			res.EmptyRows++
		} else {
			if res.FirstData == nil {
				t := at
				res.FirstData = &t
			}
			t := at
			res.LastData = &t
		}
		readings = append(readings, rd)
	}
	for _, s := range phaseSeen {
		if s {
			res.Phases++
		}
	}
	res.Quarters = quarters(deviceID, readings, res)
	res.Unattributed = unattributed(readings, res.Quarters)
	return res, nil
}

// unattributed returns, per counter, how much the meter advanced during gaps
// (so it could not be assigned to a quarter). Resets are not counted.
func unattributed(rs []reading, qs []store.Quarter) map[string]float64 {
	sel := map[string]func(r reading) *float64{
		"import_kwh": func(r reading) *float64 { return r.imp },
		"export_kwh": func(r reading) *float64 { return r.exp },
		"gas_m3":     func(r reading) *float64 { return r.gas },
		"water_m3":   func(r reading) *float64 { return r.water },
	}
	sum := map[string]float64{}
	for _, q := range qs {
		for k, v := range map[string]*float64{"import_kwh": q.ImportKWh, "export_kwh": q.ExportKWh, "gas_m3": q.GasM3, "water_m3": q.WaterM3} {
			if v != nil {
				sum[k] += *v
			}
		}
	}
	out := map[string]float64{}
	for k, get := range sel {
		var total float64
		var last *float64
		for _, r := range rs {
			v := get(r)
			if v == nil {
				continue
			}
			if last != nil && *v >= *last {
				total += *v - *last
			}
			last = v
		}
		if d := total - sum[k]; d > 1e-6 {
			out[k] = d
		}
	}
	return out
}

// resolve converts a local wall time to UTC. For the repeated hour in autumn
// occurrence 0 is the first (summer time) and 1 the second (winter time).
func resolve(wall time.Time, loc *time.Location, occurrence int) (time.Time, bool, bool) {
	var candidates []time.Time
	for offset := 14; offset >= -12; offset-- { // earliest UTC first = summer time first
		utc := wall.Add(-time.Duration(offset) * time.Hour)
		if utc.In(loc).Format("2006-01-02 15:04") == wall.Format("2006-01-02 15:04") {
			candidates = append(candidates, time.Date(utc.Year(), utc.Month(), utc.Day(), utc.Hour(), utc.Minute(), 0, 0, time.UTC))
		}
	}
	switch len(candidates) {
	case 0:
		return time.Time{}, false, false // does not exist (clocks go forward)
	case 1:
		return candidates[0], true, false
	default:
		if occurrence < len(candidates) {
			return candidates[occurrence], true, true
		}
		return time.Time{}, false, true
	}
}

func ambiguousWall(wall time.Time, loc *time.Location) bool {
	n := 0
	for offset := 14; offset >= -12; offset-- {
		utc := wall.Add(-time.Duration(offset) * time.Hour)
		if utc.In(loc).Format("2006-01-02 15:04") == wall.Format("2006-01-02 15:04") {
			n++
		}
	}
	return n > 1
}

func quarters(deviceID string, rs []reading, res *Result) []store.Quarter {
	var out []store.Quarter
	for i := 1; i < len(rs); i++ {
		prev, cur := rs[i-1], rs[i]
		if prev.at.IsZero() || cur.at.IsZero() {
			continue
		}
		if cur.at.Sub(prev.at) != 15*time.Minute || prev.at.Minute()%15 != 0 {
			if hasAny(prev) && hasAny(cur) {
				res.Gaps++
			}
			continue
		}
		q := store.Quarter{DeviceID: deviceID, Start: prev.at, Quality: "complete", Coverage: 1, Source: store.SourceImport}
		any, reset, missing := false, false, false
		delta := func(a, b *float64, dst **float64) {
			if a == nil && b == nil {
				return
			}
			if a == nil || b == nil {
				missing = true
				return
			}
			d := *b - *a
			if d < -1e-9 {
				reset = true
				return
			}
			if d < 0 {
				d = 0
			}
			*dst = &d
			any = true
		}
		delta(prev.imp, cur.imp, &q.ImportKWh)
		delta(prev.exp, cur.exp, &q.ExportKWh)
		delta(prev.gas, cur.gas, &q.GasM3)
		delta(prev.water, cur.water, &q.WaterM3)
		q.ImportLowKWh = lowDelta(prev.impLow, cur.impLow, q.ImportKWh)
		q.ExportLowKWh = lowDelta(prev.expLow, cur.expLow, q.ExportKWh)
		switch {
		case reset:
			res.Resets++
		case any:
			res.Valid++
			out = append(out, q)
		case missing && hasAny(prev) && hasAny(cur):
			res.Gaps++
		}
	}
	return out
}

// lowDelta returns the low-tariff part of a quarter's total from the T1
// counters a and b: nil without a total or when T1 went backwards.
func lowDelta(a, b, total *float64) *float64 {
	if a == nil || b == nil || total == nil || *b < *a-1e-9 {
		return nil
	}
	d := min(max(*b-*a, 0), *total)
	return &d
}

func hasAny(r reading) bool {
	return r.imp != nil || r.exp != nil || r.gas != nil || r.water != nil
}
