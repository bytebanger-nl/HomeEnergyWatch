package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/costs"
	"github.com/bytebanger-nl/homeenergywatch/internal/gateway"
	"github.com/bytebanger-nl/homeenergywatch/internal/homewizard"
	"github.com/bytebanger-nl/homeenergywatch/internal/ista"
	"github.com/bytebanger-nl/homeenergywatch/internal/report"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// runReport implements "hwlocal report": make a PDF report from the data
// directory without starting the gateway. The gateway may be running; the
// database is opened like the gateway does (and so brought up to date).
func runReport(args []string) error {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	dataDir := fs.String("data", env("HWLOCAL_DATA_DIR", "data"), "datamap met hwlocal.db (HWLOCAL_DATA_DIR)")
	tz := fs.String("tz", env("HWLOCAL_TZ", "Europe/Amsterdam"), "tijdzone")
	month := fs.String("month", "", "kalendermaand (JJJJ-MM); standaard de vorige maand")
	from := fs.String("from", "", "eerste dag (JJJJ-MM-DD), samen met -to")
	to := fs.String("to", "", "laatste dag (JJJJ-MM-DD), t/m")
	sections := fs.String("sections", "", "onderdelen, kommagescheiden (standaard alle): "+strings.Join(report.AllSections(), ","))
	title := fs.String("title", "", "titel op het voorblad")
	out := fs.String("o", "", "uitvoerbestand (standaard rapport-<periode>.pdf)")
	_ = fs.Parse(args)

	loc, err := time.LoadLocation(*tz)
	if err != nil {
		return fmt.Errorf("tijdzone %q: %w", *tz, err)
	}
	var f, t time.Time
	switch {
	case *from != "" || *to != "":
		if f, err = time.ParseInLocation("2006-01-02", *from, loc); err != nil {
			return fmt.Errorf("ongeldige -from %q", *from)
		}
		last, err := time.ParseInLocation("2006-01-02", *to, loc)
		if err != nil {
			return fmt.Errorf("ongeldige -to %q", *to)
		}
		t = last.AddDate(0, 0, 1)
	case *month != "":
		m, err := time.ParseInLocation("2006-01", *month, loc)
		if err != nil {
			return fmt.Errorf("ongeldige -month %q", *month)
		}
		f, t = report.MonthPeriod(m, loc)
	default:
		f, t = report.MonthPeriod(time.Now().In(loc).AddDate(0, -1, 0), loc)
	}
	path := filepath.Join(*dataDir, "hwlocal.db")
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("geen database in %s", *dataDir)
	}
	st, err := store.Open(path)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	if key, err := os.ReadFile(filepath.Join(*dataDir, secretKeyFile)); err == nil {
		_ = st.UseKey(ctx, key)
	}
	b := newReportBuilder(st, loc, version, func() []report.Device {
		list, _ := st.Devices(ctx)
		var out []report.Device
		for _, d := range list {
			if d.Enabled {
				out = append(out, report.Device{ID: d.ID, Name: d.Name, Kind: homewizard.KindOf(d.ProductType)})
			}
		}
		return out
	})
	req := report.Request{From: f, To: t, Title: *title}
	if *sections != "" {
		req.Sections = strings.Split(*sections, ",")
	}
	r, err := b.Build(ctx, req)
	if err != nil {
		return err
	}
	if *out == "" {
		*out = report.FileName(r.From, r.To)
	}
	file, err := os.Create(*out)
	if err != nil {
		return err
	}
	if err := r.PDF(file); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	fmt.Printf("%s: %s\n", *out, report.PeriodText(r.From, r.To))
	return nil
}

// newReportBuilder wires a report builder on its own calculators (the
// gateway passes its own, so their caches are shared).
func newReportBuilder(st *store.Store, loc *time.Location, ver string, devices func() []report.Device) *report.Builder {
	contracts := &costs.Contracts{Store: st}
	waterContracts := &costs.Contracts{Store: st, Table: store.WaterContracts}
	return &report.Builder{
		Store:      st,
		Costs:      &costs.Calculator{Store: st, Contracts: contracts, Loc: loc},
		WaterCosts: &costs.Calculator{Store: st, Contracts: waterContracts, Loc: loc},
		Scenarios:  &costs.Scenarios{Store: st},
		Ista:       &ista.Service{Store: st, Loc: loc}, // reads the stored ista data, never syncs
		Devices:    devices, Loc: loc, Version: ver,
	}
}

// reportDevices lists the gateway's devices for reports.
func reportDevices(mgr *gateway.Manager) func() []report.Device {
	return func() []report.Device {
		var out []report.Device
		for _, v := range mgr.Views() {
			out = append(out, report.Device{ID: v.ID, Name: v.Name, Kind: v.Kind})
		}
		return out
	}
}
