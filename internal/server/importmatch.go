package server

import (
	"math"
	"slices"

	"github.com/bytebanger-nl/homeenergywatch/internal/gateway"
	"github.com/bytebanger-nl/homeenergywatch/internal/importer"
)

// importCandidate is a device an export may be imported into.
type importCandidate struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// importCandidates lists the devices whose kind fits the export.
func importCandidates(views []gateway.DeviceView, res *importer.Result) []gateway.DeviceView {
	kinds := res.Kind.DeviceKinds()
	var out []gateway.DeviceView
	for _, v := range views {
		if slices.Contains(kinds, string(v.Kind)) {
			out = append(out, v)
		}
	}
	return out
}

// matchByCounter picks the device an export came from when several fit:
// a meter's counter only goes up, so its current import reading must be at
// or just above the last reading in the file. The device with the smallest
// such difference wins if it is clearly closer than the next one. ok is
// false when that cannot be decided (then the user chooses).
func matchByCounter(cands []gateway.DeviceView, res *importer.Result) (gateway.DeviceView, bool) {
	if len(cands) == 1 {
		return cands[0], true
	}
	if res.LastImportKWh == nil {
		return gateway.DeviceView{}, false
	}
	last := *res.LastImportKWh
	best, second := -1, math.Inf(1)
	bestDiff := math.Inf(1)
	for i, c := range cands {
		if c.Reading == nil {
			continue
		}
		now, ok := c.Reading.Values["energy_import_kwh"]
		if !ok {
			continue
		}
		diff := now - last
		if diff < -0.01 { // below the file: a different meter (or it was reset)
			continue
		}
		diff = math.Max(diff, 0)
		switch {
		case diff < bestDiff:
			second, bestDiff, best = bestDiff, diff, i
		case diff < second:
			second = diff
		}
	}
	if best < 0 {
		return gateway.DeviceView{}, false
	}
	// clearly closer: within 1 kWh while the next is further, or less than
	// half the next one's difference
	if !math.IsInf(second, 1) && !(bestDiff < 1 && second >= 1) && bestDiff >= second/2 {
		return gateway.DeviceView{}, false
	}
	return cands[best], true
}

func candidateList(cands []gateway.DeviceView) []importCandidate {
	out := make([]importCandidate, len(cands))
	for i, c := range cands {
		out[i] = importCandidate{ID: c.ID, Name: c.Name}
	}
	return out
}
