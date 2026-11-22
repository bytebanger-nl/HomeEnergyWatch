// Command hwsim simulates HomeWizard Energy devices for development: a P1
// meter (API v1 and v2), Energy Sockets, a kWh meter and a watermeter on API
// v1, and a Plug-In Battery on API v2. Values follow a plausible household
// pattern (base load, appliances, solar during the day). The P1 meter's
// /api/batteries (API v2) sets what the battery does (batteries.go).
//
//	go run ./cmd/hwsim
//
// API v2 devices use certificates from a test CA written to -ca-out; start
// the gateway with HWLOCAL_DEVICE_CA pointing to that file. Pairing succeeds
// a few seconds after the first attempt (a simulated button press).
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"math"
	"math/big"
	mrand "math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/grandcat/zeroconf"

	"github.com/bytebanger-nl/homeenergywatch/internal/ista"
	"github.com/bytebanger-nl/homeenergywatch/internal/mail"
)

type device struct {
	productType string
	productName string
	serial      string
	port        int
	v2Port      int // 0 = no API v2
	v1          bool

	mu          sync.Mutex
	importKWh   float64
	exportKWh   float64
	importT1    float64 // P1: the part of importKWh counted at the low tariff
	exportT1    float64
	gasM3       float64
	waterM3     float64
	powerOn     bool
	soc         float64
	cycles      float64
	lastPower   float64
	lastFlow    float64
	token       string
	pressAt     time.Time
	last        time.Time
	phases      int
	powerFn     func(t time.Time) float64
	gasLastStep time.Time
	// grid events of a P1: sags and swells per phase, power failures
	sags, swells      [3]int
	anyFail, longFail int
}

func main() {
	base := flag.Int("port", 18000, "base port; devices listen on port+1, port+2, ...")
	caOut := flag.String("ca-out", "data/hwsim-ca.pem", "where to write the test CA certificate")
	mdns := flag.Bool("mdns", false, "announce the devices via mDNS (like real devices)")
	host := flag.String("host", "", "address to listen on; empty = all interfaces, 127.0.0.1 = this machine only (no firewall prompt on Windows)")
	speed := flag.Float64("speed", 1, "room simulation: room time runs this many times faster than the clock (30: an hour in 2 minutes)")
	roomStart := flag.Float64("room-start", 17, "room simulation: temperature at the start (°C)")
	outdoor := flag.Float64("outdoor", 5, "room simulation: outdoor temperature (°C)")
	roomTau := flag.Duration("room-tau", 4*time.Hour, "room simulation: time constant of cooling towards the outdoor temperature (room time)")
	heatTau := flag.Duration("heat-tau", 45*time.Minute, "room simulation: time constant of heating by CV or air conditioner (room time)")
	acOffset := flag.Float64("ac-offset", 1.3, "room simulation: the air conditioner heats the room to its setpoint minus this (°C)")
	tadoSchedule := flag.Float64("tado-schedule", 18, "fake tado: temperature of the schedule (°C)")
	istaStale := flag.Int("ista-stale", 0, "fake ista: days before the last reading that ista has not measured (estimates, like when the radiators' readings are late)")
	mailDir := flag.String("mail-dir", "data/hwsim-mail", "fake SMTP server: where received mail is written (.eml)")
	flag.Parse()
	listenHost = *host
	rm := newRoom(roomParams{start: *roomStart, outdoor: *outdoor, tau: *roomTau, heatTau: *heatTau,
		cvSupply: 26, acOffset: *acOffset, speed: *speed})

	caCert, caKey := newCA()
	if err := os.MkdirAll(dirOf(*caOut), 0o755); err == nil {
		_ = os.WriteFile(*caOut, pemCert(caCert.Raw), 0o644)
	}

	start := time.Now()
	devices := []*device{
		{productType: "HWE-P1", productName: "P1 Meter", serial: "5c2fafa1b2c3", port: *base + 1, v2Port: *base + 443, v1: true, phases: 3,
			importKWh: 12345.678, exportKWh: 4321.123, importT1: 4938.271, exportT1: 1728.449, gasM3: 3456.789, powerFn: householdPower},
		{productType: "HWE-SKT", productName: "Energy Socket", serial: "3c39e7a1b001", port: *base + 2, v1: true, powerOn: true,
			importKWh: 123.4, powerFn: fridgePower},
		{productType: "HWE-SKT", productName: "Energy Socket", serial: "3c39e7a1b002", port: *base + 3, v1: true, powerOn: true,
			importKWh: 87.2, powerFn: washerPower},
		{productType: "HWE-KWH1", productName: "kWh meter", serial: "5c2fafa1c001", port: *base + 4, v1: true, phases: 1,
			importKWh: 3.2, exportKWh: 2345.6, powerFn: solarOnly},
		{productType: "HWE-WTR", productName: "Watermeter", serial: "5c2fafa1d001", port: *base + 5, v1: true,
			waterM3: 456.123},
		{productType: "HWE-BAT", productName: "Plug-In Battery", serial: "5c2fafa1e001", v2Port: *base + 444,
			importKWh: 321.0, exportKWh: 290.5, soc: 54, cycles: 112, powerFn: batteryPower},
	}

	for _, d := range devices {
		d.last = start
		if d.v1 {
			go serve(d.port, nil, d.v1Handler())
			log.Printf("%-8s %s  API v1 http://127.0.0.1:%d", d.productType, d.serial, d.port)
		}
		if d.v2Port != 0 {
			cert := deviceCert(caCert, caKey, d)
			go serve(d.v2Port, &tls.Config{Certificates: []tls.Certificate{cert}}, d.v2Handler())
			log.Printf("%-8s %s  API v2 https://127.0.0.1:%d", d.productType, d.serial, d.v2Port)
		}
		if *mdns {
			announce(d)
		}
	}
	priceMux := http.NewServeMux()
	priceMux.HandleFunc("/api/DayAheadPrices", nordPoolHandler)
	priceMux.HandleFunc("/v1/prices", energyZeroHandler)
	startTuyaRadiator(priceMux, *base+80)
	priceURL := fmt.Sprintf("http://127.0.0.1:%d", *base+80)
	newTadoSim(rm, *tadoSchedule).register(priceMux, priceURL)
	go serve(*base+80, nil, priceMux)
	go serve(*base+81, nil, (&ista.Sim{Username: "sim@example.org", Password: "sim", Lag: 3, Stale: *istaStale}).Handler())
	log.Printf("fake mijn.ista.nl  http://127.0.0.1:%d (set HWLOCAL_ISTA_URL to this URL and add an ista plugin with sim@example.org / sim)", *base+81)
	smtp := &mail.SimServer{Dir: *mailDir, Log: log.Printf}
	if _, err := smtp.Listen(fmt.Sprintf("%s:%d", listenHost, *base+25)); err != nil {
		log.Fatal(err)
	}
	log.Printf("SMTP     fake mail server  127.0.0.1:%d, security none, any login (mail is written to %s)", *base+25, *mailDir)
	go serve(*base+551, &tls.Config{Certificates: []tls.Certificate{selfSignedCert()}}, newMHIUnit(rm).handler())
	log.Printf("MHI      airco (WF-RAC)  https://127.0.0.1:%d (add in the PWA as an MHI device with address 127.0.0.1:%d)", *base+551, *base+551)
	log.Printf("tado     fake cloud      %s/tado (start the gateway with HWLOCAL_TADO_URL=%s/tado)", priceURL, priceURL)
	log.Printf("ROOM     %.1f °C, outdoor %.1f °C, speed %gx, schedule %.1f °C", *roomStart, *outdoor, *speed, *tadoSchedule)
	go rm.logEvery(15 * time.Second)
	log.Printf("fake Nord Pool and EnergyZero prices  http://127.0.0.1:%d (set HWLOCAL_NORDPOOL_URL and HWLOCAL_ENERGYZERO_URL to this URL)", *base+80)
	log.Printf("test CA written to %s (set HWLOCAL_DEVICE_CA to this file)", *caOut)
	var hosts []string
	for _, d := range devices {
		if d.v1 {
			hosts = append(hosts, fmt.Sprintf("127.0.0.1:%d", d.port))
		} else {
			hosts = append(hosts, fmt.Sprintf("127.0.0.1:%d", d.v2Port))
		}
	}
	log.Printf("add them to the gateway with HWLOCAL_DEVICES=%s", strings.Join(hosts, ","))
	select {}
}

// listenHost is the -host flag: the address every simulated device binds to.
var listenHost string

func serve(port int, tlsCfg *tls.Config, h http.Handler) {
	srv := &http.Server{Addr: fmt.Sprintf("%s:%d", listenHost, port), Handler: h, TLSConfig: tlsCfg, ReadHeaderTimeout: 5 * time.Second}
	var err error
	if tlsCfg != nil {
		err = srv.ListenAndServeTLS("", "")
	} else {
		err = srv.ListenAndServe()
	}
	log.Fatal(err)
}

func announce(d *device) {
	id := strings.ToLower(strings.ReplaceAll(d.productName, " ", "")) + "-" + d.serial[6:]
	if d.v1 {
		txt := []string{"api_enabled=1", "path=/api/v1", "serial=" + d.serial, "product_name=" + d.productName, "product_type=" + d.productType}
		if _, err := zeroconf.Register(id, "_hwenergy._tcp", "local.", d.port, txt, nil); err != nil {
			log.Printf("mdns: %v", err)
		}
	}
	if d.v2Port != 0 {
		txt := []string{"api_version=2.0.0", "id=" + verificationName(d), "serial=" + d.serial, "product_name=" + d.productName, "product_type=" + d.productType}
		if _, err := zeroconf.Register(id, "_homewizard._tcp", "local.", d.v2Port, txt, nil); err != nil {
			log.Printf("mdns: %v", err)
		}
	}
}

// ---------- simulated values ----------

func jitter(v float64) float64 { return v * (0.97 + 0.06*mrand.Float64()) }

func solar(t time.Time) float64 {
	h := float64(t.Hour()) + float64(t.Minute())/60
	if h < 7 || h > 19 {
		return 0
	}
	peak := 2800.0
	cloud := 0.75 + 0.25*math.Sin(float64(t.Unix())/97)
	return peak * math.Sin((h-7)/12*math.Pi) * cloud
}

func householdPower(t time.Time) float64 {
	base := 280 + 60*math.Sin(float64(t.Unix())/300)
	// cooking in the evening
	if t.Hour() >= 17 && t.Hour() < 19 && (t.Minute()/7)%2 == 0 {
		base += 1800
	}
	if t.Minute()%20 < 2 {
		base += 1200 // kettle
	}
	return jitter(base + fridgePower(t) + washerPower(t) - solar(t))
}

func fridgePower(t time.Time) float64 {
	if (t.Minute()/6)%3 == 0 {
		return jitter(95)
	}
	return 1.5
}

func washerPower(t time.Time) float64 {
	m := t.Minute()
	if t.Hour()%3 == 0 && m < 25 {
		if m < 8 {
			return jitter(2000)
		}
		return jitter(180)
	}
	return 0.4
}

func solarOnly(t time.Time) float64 { return -jitter(solar(t)) + 2 }

func batteryPower(t time.Time) float64 {
	s := solar(t)
	if s > 600 {
		return jitter(math.Min(800, s-300)) // charge from surplus
	}
	if t.Hour() >= 17 || t.Hour() < 7 {
		return -jitter(350) // discharge to cover base load
	}
	return 0
}

// lowTariff reports whether a Dutch meter is at its low tariff at t:
// weekdays 23:00-07:00, weekends and (a few fixed) public holidays.
func lowTariff(t time.Time) bool {
	switch t.Format("01-02") {
	case "01-01", "04-27", "12-25", "12-26":
		return true
	}
	return t.Hour() < 7 || t.Hour() >= 23 || t.Weekday() == time.Saturday || t.Weekday() == time.Sunday
}

// tick integrates counters up to now.
func (d *device) tick() {
	now := time.Now()
	dt := now.Sub(d.last).Hours()
	if dt <= 0 {
		return
	}
	d.last = now
	switch d.productType {
	case "HWE-WTR":
		flow := 0.0
		if now.Minute()%15 < 2 {
			flow = jitter(8)
		} else if mrand.Float64() < 0.05 {
			flow = jitter(1.5)
		}
		d.lastFlow = flow
		d.waterM3 += flow * dt * 60 / 1000
		return
	}
	p := 0.0
	if d.powerFn != nil {
		p = d.powerFn(now)
	}
	if d.productType == "HWE-SKT" && !d.powerOn {
		p = 0
	}
	if d.productType == "HWE-BAT" {
		p = batteries.apply(p, d.soc) // the stand the P1 meter set
		if (p > 0 && d.soc >= 100) || (p < 0 && d.soc <= 5) {
			p = 0
		}
		d.soc = math.Max(0, math.Min(100, d.soc+p*dt/2700*100))
		d.cycles += math.Abs(p) * dt / 2700 / 2
	}
	d.lastPower = p
	low := d.productType == "HWE-P1" && lowTariff(now)
	if p >= 0 {
		d.importKWh += p * dt / 1000
		if low {
			d.importT1 += p * dt / 1000
		}
	} else {
		d.exportKWh += -p * dt / 1000
		if low {
			d.exportT1 += -p * dt / 1000
		}
	}
	if d.productType == "HWE-P1" {
		// now and then a voltage sag or swell on a random phase (about one
		// an hour each at 1 s polling), and a short power failure rarely
		if mrand.Float64() < dt/3600 {
			d.sags[mrand.IntN(3)]++
		}
		if mrand.Float64() < dt/3600 {
			d.swells[mrand.IntN(3)]++
		}
		if mrand.Float64() < dt/(6*3600) {
			d.anyFail++
		}
	}
	if d.productType == "HWE-P1" && now.Sub(d.gasLastStep) >= 5*time.Minute {
		d.gasLastStep = now.Truncate(5 * time.Minute)
		hour := now.Hour()
		if hour < 9 || hour > 16 {
			d.gasM3 += 0.04 + 0.03*mrand.Float64()
		} else {
			d.gasM3 += 0.01
		}
	}
}

func round(v float64, digits int) float64 {
	p := math.Pow(10, float64(digits))
	return math.Round(v*p) / p
}

func (d *device) info(v2 bool) map[string]any {
	api := "v1"
	if v2 {
		api = "2.0.0"
	}
	return map[string]any{"product_name": d.productName, "product_type": d.productType, "serial": d.serial,
		"firmware_version": "6.00", "api_version": api}
}

func (d *device) measurement(v2 bool) map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.tick()
	m := map[string]any{}
	now := time.Now()
	key := func(v1, v2name string) string {
		if v2 {
			return v2name
		}
		return v1
	}
	if !v2 {
		m["wifi_ssid"] = "Thuis"
		m["wifi_strength"] = 78
	}
	switch d.productType {
	case "HWE-WTR":
		m["active_liter_lpm"] = round(d.lastFlow, 1)
		m["total_liter_m3"] = round(d.waterM3, 3)
		return m
	}
	p := d.lastPower
	m[key("total_power_import_kwh", "energy_import_kwh")] = round(d.importKWh, 3)
	m[key("total_power_export_kwh", "energy_export_kwh")] = round(d.exportKWh, 3)
	m[key("active_power_w", "power_w")] = round(p, 1)
	switch d.phases {
	case 3:
		split := []float64{0.5, 0.3, 0.2}
		for i, f := range split {
			n := i + 1
			m[key(fmt.Sprintf("active_power_l%d_w", n), fmt.Sprintf("power_l%d_w", n))] = round(p*f, 1)
			v := 229 + 3*math.Sin(float64(now.Unix())/40+float64(i))
			m[key(fmt.Sprintf("active_voltage_l%d_v", n), fmt.Sprintf("voltage_l%d_v", n))] = round(v, 1)
			m[key(fmt.Sprintf("active_current_l%d_a", n), fmt.Sprintf("current_l%d_a", n))] = round(math.Abs(p*f)/v, 2)
		}
	case 1:
		v := 230.4
		m[key("active_voltage_v", "voltage_v")] = v
		m[key("active_current_a", "current_a")] = round(math.Abs(p)/v, 2)
		m[key("active_frequency_hz", "frequency_hz")] = 50.0
	}
	if d.productType == "HWE-P1" {
		tariff := 2
		if lowTariff(now) {
			tariff = 1
		}
		m[key("active_tariff", "tariff")] = tariff
		// the totals per tariff, as a real meter reports them: T1 (low)
		// only counts while the tariff is low
		for _, dir := range []string{"import", "export"} {
			total, t1 := d.importKWh, d.importT1
			if dir == "export" {
				total, t1 = d.exportKWh, d.exportT1
			}
			m[key("total_power_"+dir+"_t1_kwh", "energy_"+dir+"_t1_kwh")] = round(t1, 3)
			m[key("total_power_"+dir+"_t2_kwh", "energy_"+dir+"_t2_kwh")] = round(total-round(t1, 3), 3)
		}
		m[key("active_power_average_w", "average_power_15m_w")] = round(math.Max(p, 0)*0.9, 0)
		m[key("montly_power_peak_w", "monthly_power_peak_w")] = 4120.0
		m[key("smr_version", "protocol_version")] = 50
		m["meter_model"] = "ISKRA 2M550T-1013"
		for i := range 3 {
			m[fmt.Sprintf("voltage_sag_l%d_count", i+1)] = d.sags[i]
			m[fmt.Sprintf("voltage_swell_l%d_count", i+1)] = d.swells[i]
		}
		m["any_power_fail_count"] = d.anyFail
		m["long_power_fail_count"] = d.longFail
		gasTS := d.gasLastStep.In(amsterdam())
		m["external"] = []map[string]any{{
			"unique_id": hex.EncodeToString([]byte("G0012345678901234")), "type": "gas_meter",
			"value": round(d.gasM3, 3), "unit": "m3", "timestamp": gasTimestamp(gasTS),
		}}
		if !v2 {
			m["total_gas_m3"] = round(d.gasM3, 3)
			m["gas_timestamp"] = gasTimestamp(gasTS)
		}
	}
	if d.productType == "HWE-BAT" {
		m["state_of_charge_pct"] = round(d.soc, 1)
		m["cycles"] = math.Floor(d.cycles)
	}
	return m
}

func gasTimestamp(t time.Time) int64 {
	var n int64
	fmt.Sscanf(t.Format("060102150405"), "%d", &n)
	return n
}

var amsterdamLoc *time.Location

func amsterdam() *time.Location {
	if amsterdamLoc == nil {
		if l, err := time.LoadLocation("Europe/Amsterdam"); err == nil {
			amsterdamLoc = l
		} else {
			amsterdamLoc = time.Local
		}
	}
	return amsterdamLoc
}

// ---------- fake day-ahead prices ----------

// nordPoolHandler mimics the Nord Pool data portal API with a typical Dutch
// day: cheap at night, a solar dip (sometimes negative) around noon and an
// evening peak. Tomorrow is published from 13:00.
func nordPoolHandler(w http.ResponseWriter, r *http.Request) {
	loc := amsterdam()
	day, err := time.ParseInLocation("2006-01-02", r.URL.Query().Get("date"), loc)
	if err != nil {
		http.Error(w, "bad date", http.StatusBadRequest)
		return
	}
	now := time.Now().In(loc)
	if day.After(now) && (day.Sub(now) > 24*time.Hour || now.Hour() < 13) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var entries []map[string]any
	for t := day; t.Before(day.AddDate(0, 0, 1)); t = t.Add(15 * time.Minute) {
		entries = append(entries, map[string]any{
			"deliveryStart": t.UTC().Format(time.RFC3339), "deliveryEnd": t.Add(15 * time.Minute).UTC().Format(time.RFC3339),
			"entryPerArea": map[string]float64{"NL": round(simPrice(t), 2)},
		})
	}
	writeJSON(w, 200, map[string]any{"deliveryDateCET": day.Format("2006-01-02"), "market": "DayAhead", "currency": "EUR",
		"deliveryAreas": []string{"NL"}, "multiAreaEntries": entries})
}

// simPrice is the fake day-ahead price (EUR/MWh) of the quarter starting at t.
func simPrice(t time.Time) float64 {
	t = t.In(amsterdam())
	seed := float64(t.YearDay())
	h := float64(t.Hour()) + float64(t.Minute())/60
	p := 85 + 25*math.Sin((h-4)/24*2*math.Pi)                        // daily shape
	p += 70 * math.Exp(-math.Pow(h-19, 2)/3)                         // evening peak
	p += 35 * math.Exp(-math.Pow(h-8, 2)/1.5)                        // morning peak
	p -= (60 + 30*math.Sin(seed)) * math.Exp(-math.Pow(h-13.5, 2)/4) // solar dip
	p += 6 * math.Sin(h*3+seed)
	return p
}

// energyZeroHandler mimics the public EnergyZero price API: electricity per
// quarter hour (EUR/kWh) or gas per gas day (EUR/m³, 06:00-06:00), for the
// requested date plus the day before and after when "published" (tomorrow
// from 13:00).
func energyZeroHandler(w http.ResponseWriter, r *http.Request) {
	loc := amsterdam()
	q := r.URL.Query()
	day, err := time.ParseInLocation("02-01-2006", q.Get("date"), loc)
	if err != nil {
		http.Error(w, "bad date", http.StatusBadRequest)
		return
	}
	gas := q.Get("energy_type") == "ENERGY_TYPE_GAS"
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	last := today // last published calendar day
	if now.Hour() >= 13 {
		last = today.AddDate(0, 0, 1)
	}
	if day.After(last) {
		writeJSON(w, http.StatusNotFound, map[string]any{"code": 5, "message": "prices do not span the whole requested date", "details": []any{}})
		return
	}
	from, to := day.AddDate(0, 0, -1), day.AddDate(0, 0, 2)
	if to.After(last.AddDate(0, 0, 1)) {
		to = last.AddDate(0, 0, 1)
	}
	type entry struct {
		Start string            `json:"start"`
		End   string            `json:"end"`
		Price map[string]string `json:"price"`
	}
	var base []entry
	interval := "RESPONSE_INTERVAL_QUARTER"
	if gas {
		interval = "RESPONSE_INTERVAL_DAY"
		// gas day d runs from d 06:00 to d+1 06:00 and is published with date d
		for d := day.AddDate(0, 0, -1); !d.After(day.AddDate(0, 0, 1)) && !d.After(last); d = d.AddDate(0, 0, 1) {
			s := time.Date(d.Year(), d.Month(), d.Day(), 6, 0, 0, 0, loc)
			v := 0.32 + 0.04*math.Sin(float64(s.YearDay())/7)
			base = append(base, entry{s.UTC().Format(time.RFC3339), s.AddDate(0, 0, 1).UTC().Format(time.RFC3339), map[string]string{"value": strconv.FormatFloat(round(v, 6), 'f', -1, 64)}})
		}
	} else {
		for t := from; t.Before(to); t = t.Add(15 * time.Minute) {
			v := strconv.FormatFloat(round(simPrice(t)/1000, 5), 'f', -1, 64)
			base = append(base, entry{t.UTC().Format(time.RFC3339), t.Add(15 * time.Minute).UTC().Format(time.RFC3339), map[string]string{"value": v}})
		}
	}
	writeJSON(w, 200, map[string]any{"interval": interval, "base": base})
}

// ---------- HTTP handlers ----------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (d *device) v1Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, d.info(false)) })
	mux.HandleFunc("GET /api/v1/data", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, d.measurement(false)) })
	mux.HandleFunc("PUT /api/v1/identify", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s: identify (blink)", d.productType, d.serial)
		writeJSON(w, 200, map[string]bool{"identify": true})
	})
	if d.productType == "HWE-SKT" {
		state := func() map[string]any {
			return map[string]any{"power_on": d.powerOn, "switch_lock": false, "brightness": 255}
		}
		mux.HandleFunc("GET /api/v1/state", func(w http.ResponseWriter, r *http.Request) {
			d.mu.Lock()
			defer d.mu.Unlock()
			writeJSON(w, 200, state())
		})
		mux.HandleFunc("PUT /api/v1/state", func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				PowerOn *bool `json:"power_on"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				writeJSON(w, 400, map[string]any{"error": map[string]any{"id": 2, "description": "Body contains invalid json"}})
				return
			}
			d.mu.Lock()
			defer d.mu.Unlock()
			d.tick()
			if body.PowerOn != nil {
				d.powerOn = *body.PowerOn
				log.Printf("%s %s: power_on=%v", d.productType, d.serial, d.powerOn)
			}
			writeJSON(w, 200, state())
		})
	}
	return mux
}

func (d *device) v2Handler() http.Handler {
	mux := http.NewServeMux()
	authed := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			d.mu.Lock()
			ok := d.token != "" && r.Header.Get("Authorization") == "Bearer "+d.token
			d.mu.Unlock()
			if !ok {
				writeJSON(w, 401, map[string]string{"error": "user:unauthorized"})
				return
			}
			next(w, r)
		}
	}
	mux.HandleFunc("GET /api", authed(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, d.info(true)) }))
	mux.HandleFunc("GET /api/measurement", authed(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, d.measurement(true)) }))
	mux.HandleFunc("PUT /api/system/identify", authed(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	if d.productType == "HWE-P1" {
		mux.HandleFunc("GET /api/batteries", authed(batteries.get))
		mux.HandleFunc("PUT /api/batteries", authed(batteries.put))
	}
	mux.HandleFunc("POST /api/user", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		now := time.Now()
		if d.pressAt.IsZero() || now.Sub(d.pressAt) > 60*time.Second {
			d.pressAt = now.Add(4 * time.Second) // the user "presses the button" after 4 s
			log.Printf("%s %s: pairing requested, simulating button press in 4 s", d.productType, d.serial)
		}
		if now.Before(d.pressAt) {
			writeJSON(w, 403, map[string]string{"error": "user:creation-not-enabled"})
			return
		}
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		d.token = strings.ToUpper(hex.EncodeToString(b))
		d.pressAt = time.Time{}
		log.Printf("%s %s: paired", d.productType, d.serial)
		writeJSON(w, 200, map[string]string{"token": d.token, "name": "local/homeenergywatch"})
	})
	return mux
}

// ---------- certificates ----------

func verificationName(d *device) string {
	kind := map[string]string{"HWE-P1": "p1dongle", "HWE-BAT": "battery", "HWE-KWH1": "energymeter"}[d.productType]
	return "appliance/" + kind + "/" + d.serial
}

func newCA() (*x509.Certificate, *ecdsa.PrivateKey) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "hwsim Appliance Access CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(1, 0, 0),
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		log.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return cert, key
}

// deviceCert mimics HomeWizard: the identity is in the common name only.
func deviceCert(ca *x509.Certificate, caKey *ecdsa.PrivateKey, d *device) tls.Certificate {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: verificationName(d)},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(1, 0, 0),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		log.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func pemCert(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func dirOf(path string) string {
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		return path[:i]
	}
	return "."
}
