// Command hwlocal is the local HomeWizard gateway: it discovers and polls
// HomeWizard Energy devices on the LAN, keeps their history and serves the
// PWA for realtime insight.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // time zone data for Windows and minimal containers

	"github.com/bytebanger-nl/homeenergywatch/internal/auth"
	"github.com/bytebanger-nl/homeenergywatch/internal/control"
	"github.com/bytebanger-nl/homeenergywatch/internal/costs"
	"github.com/bytebanger-nl/homeenergywatch/internal/discovery"
	"github.com/bytebanger-nl/homeenergywatch/internal/forecast"
	"github.com/bytebanger-nl/homeenergywatch/internal/gateway"
	"github.com/bytebanger-nl/homeenergywatch/internal/history"
	"github.com/bytebanger-nl/homeenergywatch/internal/homewizard"
	"github.com/bytebanger-nl/homeenergywatch/internal/ista"
	"github.com/bytebanger-nl/homeenergywatch/internal/mail"
	"github.com/bytebanger-nl/homeenergywatch/internal/notice"
	"github.com/bytebanger-nl/homeenergywatch/internal/outage"
	"github.com/bytebanger-nl/homeenergywatch/internal/plugin"
	"github.com/bytebanger-nl/homeenergywatch/internal/plugin/hwbattery"
	_ "github.com/bytebanger-nl/homeenergywatch/internal/plugin/mhi"  // register the MHI provider
	"github.com/bytebanger-nl/homeenergywatch/internal/plugin/tado"   // register the tado provider
	_ "github.com/bytebanger-nl/homeenergywatch/internal/plugin/tuya" // register the Tuya provider
	"github.com/bytebanger-nl/homeenergywatch/internal/prices"
	"github.com/bytebanger-nl/homeenergywatch/internal/report"
	"github.com/bytebanger-nl/homeenergywatch/internal/rules"
	"github.com/bytebanger-nl/homeenergywatch/internal/server"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
	"github.com/bytebanger-nl/homeenergywatch/internal/tlsconfig"
	"github.com/bytebanger-nl/homeenergywatch/internal/tsserve"
	"github.com/bytebanger-nl/homeenergywatch/internal/webpush"
	"github.com/bytebanger-nl/homeenergywatch/web"
)

// version is set at build time with -ldflags "-X main.version=..."
var version = "dev"

type config struct {
	dataDir        string
	httpAddr       string
	httpsAddr      string
	tlsMode        string
	domain         string
	acmeEmail      string
	acmeStaging    bool
	cfToken        string
	tlsNames       string
	interval       time.Duration
	discovery      bool
	discoveryEvery time.Duration
	retentionDays  int
	timeZone       string
	allowPublic    bool
	allowTailscale bool
	allowedHosts   string
	pushContact    string
	logLevel       string
	devices        string
	priceSources   string
	gasSources     string
	priceBackfill  string
	entsoeToken    string
	backupDir      string
	backupKeep     int
	backupTime     string
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	if v, ok := os.LookupEnv(key); ok {
		b, err := strconv.ParseBool(v)
		if err == nil {
			return b
		}
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func envInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// parseConfig reads the flags in args, with HWLOCAL_* environment variables
// as defaults. defaultData is the data directory when neither is set.
func parseConfig(fs *flag.FlagSet, args []string, defaultData string) (config, error) {
	var c config
	fs.StringVar(&c.dataDir, "data", env("HWLOCAL_DATA_DIR", defaultData), "map voor database, tokens en certificaten (HWLOCAL_DATA_DIR)")
	fs.StringVar(&c.httpAddr, "http", env("HWLOCAL_HTTP_ADDR", ":8080"), "HTTP-adres (HWLOCAL_HTTP_ADDR); leeg = uit")
	fs.StringVar(&c.httpsAddr, "https", env("HWLOCAL_HTTPS_ADDR", ":8443"), "HTTPS-adres als TLS aan staat (HWLOCAL_HTTPS_ADDR)")
	fs.StringVar(&c.tlsMode, "tls", env("HWLOCAL_TLS", ""), "TLS: off, acme of selfsigned (HWLOCAL_TLS); standaard acme als een domein is ingesteld, anders off")
	fs.StringVar(&c.domain, "domain", env("HWLOCAL_DOMAIN", ""), "domeinnaam voor Let's Encrypt, bijv. energie.voorbeeld.nl (HWLOCAL_DOMAIN)")
	fs.StringVar(&c.acmeEmail, "acme-email", env("HWLOCAL_ACME_EMAIL", ""), "e-mail voor Let's Encrypt-meldingen (HWLOCAL_ACME_EMAIL)")
	fs.BoolVar(&c.acmeStaging, "acme-staging", envBool("HWLOCAL_ACME_STAGING", false), "Let's Encrypt staging gebruiken (HWLOCAL_ACME_STAGING)")
	fs.StringVar(&c.tlsNames, "tls-names", env("HWLOCAL_TLS_NAMES", ""), "extra namen/IP's voor het selfsigned certificaat, kommagescheiden (HWLOCAL_TLS_NAMES)")
	fs.DurationVar(&c.interval, "interval", envDuration("HWLOCAL_INTERVAL", 2*time.Second), "uitleesinterval per apparaat, 1s-60s (HWLOCAL_INTERVAL)")
	fs.BoolVar(&c.discovery, "discovery", envBool("HWLOCAL_DISCOVERY", true), "apparaten automatisch zoeken via mDNS (HWLOCAL_DISCOVERY)")
	fs.DurationVar(&c.discoveryEvery, "discovery-interval", envDuration("HWLOCAL_DISCOVERY_INTERVAL", 5*time.Minute), "hoe vaak opnieuw zoeken (HWLOCAL_DISCOVERY_INTERVAL)")
	fs.IntVar(&c.retentionDays, "retention-days", envInt("HWLOCAL_RETENTION_DAYS", 400), "bewaartermijn minuutdata in dagen; kwartierdata blijft altijd (HWLOCAL_RETENTION_DAYS)")
	fs.StringVar(&c.timeZone, "tz", env("HWLOCAL_TZ", "Europe/Amsterdam"), "tijdzone voor dagtotalen (HWLOCAL_TZ)")
	fs.BoolVar(&c.allowPublic, "allow-public", envBool("HWLOCAL_ALLOW_PUBLIC", false), "ook verzoeken van buiten het lokale netwerk toestaan (HWLOCAL_ALLOW_PUBLIC)")
	fs.BoolVar(&c.allowTailscale, "allow-tailscale", envBool("HWLOCAL_ALLOW_TAILSCALE", false), "ook verzoeken van Tailscale-adressen (100.64.0.0/10) toestaan (HWLOCAL_ALLOW_TAILSCALE)")
	fs.StringVar(&c.allowedHosts, "allowed-hosts", env("HWLOCAL_ALLOWED_HOSTS", ""), "extra hostnamen waarop de gateway bereikbaar is, kommagescheiden, of '*' (HWLOCAL_ALLOWED_HOSTS)")
	fs.StringVar(&c.pushContact, "push-contact", env("HWLOCAL_PUSH_CONTACT", defaultPushContact), "contact voor de pushdiensten van browsers, mailto: of https: (HWLOCAL_PUSH_CONTACT)")
	fs.StringVar(&c.logLevel, "log", env("HWLOCAL_LOG_LEVEL", "info"), "logniveau: debug, info, warn, error (HWLOCAL_LOG_LEVEL)")
	fs.StringVar(&c.devices, "devices", env("HWLOCAL_DEVICES", ""), "apparaten om bij start toe te voegen, kommagescheiden IP's (HWLOCAL_DEVICES)")
	fs.StringVar(&c.priceSources, "price-sources", env("HWLOCAL_PRICE_SOURCES", defaultPriceSources), "bronnen voor dynamische stroomprijzen in volgorde, of 'off' (HWLOCAL_PRICE_SOURCES)")
	fs.StringVar(&c.gasSources, "gas-price-sources", env("HWLOCAL_GAS_PRICE_SOURCES", "energyzero"), "bron voor gasprijzen per gasdag, of 'off' (HWLOCAL_GAS_PRICE_SOURCES)")
	fs.StringVar(&c.priceBackfill, "price-backfill-from", env("HWLOCAL_PRICE_BACKFILL_FROM", "2026-01-01"), "ontbrekende prijzen vanaf deze dag ophalen (JJJJ-MM-DD), of 'off' (HWLOCAL_PRICE_BACKFILL_FROM)")
	fs.StringVar(&c.backupDir, "backup-dir", env("HWLOCAL_BACKUP_DIR", ""), "map voor de dagelijkse back-up, bij voorkeur op een andere schijf; standaard <datamap>/backups, 'off' = uit (HWLOCAL_BACKUP_DIR)")
	fs.IntVar(&c.backupKeep, "backup-keep", envInt("HWLOCAL_BACKUP_KEEP", 7), "aantal back-ups dat bewaard blijft (HWLOCAL_BACKUP_KEEP)")
	fs.StringVar(&c.backupTime, "backup-time", env("HWLOCAL_BACKUP_TIME", "03:30"), "tijdstip van de dagelijkse back-up, UU:MM (HWLOCAL_BACKUP_TIME)")
	showVersion := fs.Bool("version", false, "versie tonen")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if fs.NArg() > 0 {
		return c, fmt.Errorf("onverwacht argument %q", fs.Arg(0))
	}
	if *showVersion {
		fmt.Println("hwlocal", version)
		os.Exit(0)
	}
	c.cfToken = os.Getenv("CLOUDFLARE_API_TOKEN")
	c.entsoeToken = os.Getenv("ENTSOE_API_TOKEN")
	if c.tlsMode == "" {
		if c.domain != "" {
			c.tlsMode = "acme"
		} else {
			c.tlsMode = "off"
		}
	}
	return c, nil
}

const defaultPriceSources = "energyzero,nordpool,entsoe"

// entsoeTokenFile in the data directory holds the ENTSO-E token when
// ENTSOE_API_TOKEN is not set.
const entsoeTokenFile = "entsoe-token.txt"

// accessFile in the data directory holds the remote-access settings made
// in the PWA (Tailscale on/off, extra host names).
const accessFile = "access.json"

// tabOrderFile in the data directory holds the order of the PWA's tab row.
const tabOrderFile = "tab-order.json"

// authFile in the data directory holds the optional password (hashed), the
// sessions of the browsers that logged in and the API tokens (see
// internal/auth).
const authFile = "auth.json"

// secretKeyFile in the data directory is the key plugin passwords and keys
// are encrypted with in the database.
const secretKeyFile = "secret.key"

// priceSources builds the configured electricity price sources.
func priceSources(names, entsoeToken string) ([]prices.Source, error) {
	var out []prices.Source
	for _, name := range strings.Split(names, ",") {
		switch strings.TrimSpace(strings.ToLower(name)) {
		case "", "off":
		case "energyzero":
			out = append(out, &prices.EnergyZero{BaseURL: os.Getenv("HWLOCAL_ENERGYZERO_URL")})
		case "nordpool":
			out = append(out, &prices.NordPool{BaseURL: os.Getenv("HWLOCAL_NORDPOOL_URL")})
		case "entsoe":
			if entsoeToken == "" {
				continue // optional fallback; skipped without a token
			}
			out = append(out, &prices.EntsoE{Token: entsoeToken})
		default:
			return nil, fmt.Errorf("onbekende prijsbron %q (energyzero, nordpool, entsoe)", name)
		}
	}
	return out, nil
}

// gasSources builds the configured gas price sources.
func gasSources(names string) ([]prices.Source, error) {
	var out []prices.Source
	for _, name := range strings.Split(names, ",") {
		switch strings.TrimSpace(strings.ToLower(name)) {
		case "", "off":
		case "energyzero":
			out = append(out, &prices.EnergyZero{BaseURL: os.Getenv("HWLOCAL_ENERGYZERO_URL"), Gas: true})
		default:
			return nil, fmt.Errorf("onbekende gasprijsbron %q (energyzero)", name)
		}
	}
	return out, nil
}

// allowedHosts lists the host names the PWA may be opened by besides IP
// addresses and LAN names: the Let's Encrypt domain, the self-signed
// certificate names, this machine's host name and HWLOCAL_ALLOWED_HOSTS.
func allowedHosts(cfg config) []string {
	var out []string
	for _, list := range []string{cfg.domain, cfg.tlsNames, cfg.allowedHosts} {
		for _, h := range strings.Split(list, ",") {
			if h = strings.TrimSpace(h); h != "" {
				out = append(out, h)
			}
		}
	}
	if name, err := os.Hostname(); err == nil && name != "" {
		out = append(out, name)
	}
	return out
}

// backfillFrom parses HWLOCAL_PRICE_BACKFILL_FROM ("off" disables it).
func backfillFrom(v string) (time.Time, error) {
	switch strings.TrimSpace(strings.ToLower(v)) {
	case "", "off":
		return time.Time{}, nil
	}
	d, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(v), prices.Amsterdam)
	if err != nil {
		return time.Time{}, fmt.Errorf("ongeldige HWLOCAL_PRICE_BACKFILL_FROM %q (JJJJ-MM-DD)", v)
	}
	return d, nil
}

// runPrices implements "hwlocal prices": fetch and print the prices of a day
// from every configured source, without starting the gateway.
func runPrices(args []string) error {
	fs := flag.NewFlagSet("prices", flag.ExitOnError)
	date := fs.String("date", time.Now().In(prices.Amsterdam).Format("2006-01-02"), "leverdag (JJJJ-MM-DD, Nederlandse tijd); bij gas de gasdag die op die datum om 06:00 begint")
	gasFlag := fs.Bool("gas", false, "gasprijs per gasdag in plaats van stroom")
	sources := fs.String("sources", "", "bronnen, kommagescheiden (standaard HWLOCAL_PRICE_SOURCES of HWLOCAL_GAS_PRICE_SOURCES)")
	_ = fs.Parse(args)
	day, err := time.ParseInLocation("2006-01-02", *date, prices.Amsterdam)
	if err != nil {
		return fmt.Errorf("ongeldige datum %q", *date)
	}
	c := prices.Electricity
	var list []prices.Source
	if *gasFlag {
		c = prices.Gas
		if *sources == "" {
			*sources = env("HWLOCAL_GAS_PRICE_SOURCES", "energyzero")
		}
		list, err = gasSources(*sources)
	} else {
		if *sources == "" {
			*sources = env("HWLOCAL_PRICE_SOURCES", defaultPriceSources)
		}
		list, err = priceSources(*sources, os.Getenv("ENTSOE_API_TOKEN"))
		if strings.Contains(*sources, "entsoe") && os.Getenv("ENTSOE_API_TOKEN") == "" {
			fmt.Println("entsoe: overgeslagen, ENTSOE_API_TOKEN is niet gezet")
		}
	}
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	from, to := c.Bounds(day)
	for _, src := range list {
		all, err := src.Day(ctx, day)
		if err != nil {
			fmt.Printf("%s: %v\n", src.Name(), err)
			continue
		}
		var ps []prices.Price // only the requested day; sources may return more
		for _, p := range all {
			if !p.Start.Before(from) && p.Start.Before(to) {
				ps = append(ps, p)
			}
		}
		if len(ps) == 0 {
			fmt.Printf("%s: geen prijzen voor deze dag\n", src.Name())
			continue
		}
		minP, maxP, sum := ps[0], ps[0], 0.0
		var dur time.Duration
		for _, p := range ps {
			if p.Value < minP.Value {
				minP = p
			}
			if p.Value > maxP.Value {
				maxP = p
			}
			sum += p.Value * p.Duration.Hours()
			dur += p.Duration
		}
		fmt.Printf("%s: %d perioden van %s, %s tot %s, gemiddeld %.4f %s, laagst %.4f (%s), hoogst %.4f (%s)\n",
			src.Name(), len(ps), ps[0].Duration, from.In(prices.Amsterdam).Format("02-01 15:04"), to.In(prices.Amsterdam).Format("02-01 15:04"),
			sum/dur.Hours(), c.Unit, minP.Value, minP.Start.In(prices.Amsterdam).Format("15:04"), maxP.Value, maxP.Start.In(prices.Amsterdam).Format("15:04"))
		for i, p := range ps {
			switch {
			case i < 8 || i >= len(ps)-4:
				if *gasFlag {
					fmt.Printf("  %s  %8.4f EUR/m3\n", p.Start.In(prices.Amsterdam).Format("2006-01-02 15:04"), p.Value)
				} else {
					fmt.Printf("  %s  %8.2f EUR/MWh  %7.4f EUR/kWh\n", p.Start.In(prices.Amsterdam).Format("2006-01-02 15:04"), p.Value, p.Value/1000)
				}
			case i == 8:
				fmt.Println("  ...")
			}
		}
	}
	return nil
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "prices" {
		if err := runPrices(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "report" {
		if err := runReport(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "restore" {
		if err := runRestore(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "password" {
		if err := runPassword(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "service" {
		if err := serviceCommand(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if isService() {
		runService()
		return
	}
	cfg, err := parseConfig(flag.CommandLine, os.Args[1:], "data")
	if err != nil {
		os.Exit(2) // the flag package has printed the error
	}
	log := newLogger(cfg.logLevel, os.Stderr)
	slog.SetDefault(log)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg, log); err != nil {
		log.Error("gestopt", "err", err)
		os.Exit(1)
	}
}

func newLogger(level string, w io.Writer) *slog.Logger {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		l = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: l}))
}

// run starts the gateway and blocks until ctx is cancelled or a listener
// fails.
func run(ctx context.Context, cfg config, log *slog.Logger) error {
	if cfg.interval < time.Second || cfg.interval > time.Minute {
		return fmt.Errorf("interval %s valt buiten 1s-60s", cfg.interval)
	}
	loc, err := time.LoadLocation(cfg.timeZone)
	if err != nil {
		return fmt.Errorf("tijdzone %q: %w", cfg.timeZone, err)
	}
	dataDir, err := filepath.Abs(cfg.dataDir)
	if err != nil {
		return err
	}

	ctx, stop := context.WithCancel(ctx)
	defer stop()

	st, err := store.Open(filepath.Join(dataDir, "hwlocal.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	// plugin passwords and keys are stored encrypted with this key
	secretKey, err := store.LoadOrCreateKey(filepath.Join(dataDir, secretKeyFile))
	if err != nil {
		return fmt.Errorf("sleutel: %w", err)
	}
	if err := st.UseKey(ctx, secretKey); err != nil {
		return fmt.Errorf("sleutel: %w", err)
	}

	rec := history.NewRecorder(st, log, history.Config{
		Retention: time.Duration(cfg.retentionDays) * 24 * time.Hour,
		Location:  loc,
	})
	clientOpts := homewizard.Options{Timeout: 5 * time.Second}
	if path := os.Getenv("HWLOCAL_DEVICE_CA"); path != "" {
		// extra CA for API v2 devices, e.g. the simulator's test CA
		pem, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("HWLOCAL_DEVICE_CA: %w", err)
		}
		pool := homewizard.HomeWizardCA()
		if !pool.AppendCertsFromPEM(pem) {
			return fmt.Errorf("HWLOCAL_DEVICE_CA: geen geldig PEM-certificaat in %s", path)
		}
		clientOpts.RootCAs = pool
		log.Warn("extra CA voor apparaten geladen (alleen voor ontwikkeling)", "file", path)
	}
	// costs are cached per day; stored quarter hours, prices and contract
	// changes drop the cache from the first affected day
	contracts := &costs.Contracts{Store: st}
	forecastSettings := &forecast.Service{Store: st}
	calc := &costs.Calculator{Store: st, Contracts: contracts, Loc: loc, Settle: rec.Settle(), Forecast: forecastSettings}
	contracts.OnChange = calc.InvalidateDate
	waterContracts := &costs.Contracts{Store: st, Table: store.WaterContracts}
	waterCalc := &costs.Calculator{Store: st, Contracts: waterContracts, Loc: loc, Settle: rec.Settle(), Forecast: forecastSettings}
	waterContracts.OnChange = waterCalc.InvalidateDate
	rec.OnQuarters = func(deviceID string, from time.Time) {
		calc.Invalidate(deviceID, from)
		waterCalc.Invalidate(deviceID, from)
	}

	// when devices did not respond; a stop of the gateway is not counted
	hub := gateway.NewHub()
	outages := outage.New(st, log)
	outages.OnChange = func() { hub.Publish(gateway.Event{Type: "outages"}) }
	// messages about what went wrong (a report not mailed, a backup failed, ...)
	notices := notice.New(st, log)
	notices.OnChange = func() { hub.Publish(gateway.Event{Type: "notices"}) }
	if err := outages.Start(ctx); err != nil {
		return err
	}
	go outages.Run(ctx)
	mgr := gateway.NewManager(st, rec, hub, log, gateway.Config{
		Interval: cfg.interval,
		Client:   clientOpts,
	})
	mgr.Outages = outages
	if err := mgr.Start(ctx); err != nil {
		return err
	}
	for _, host := range strings.Split(cfg.devices, ",") {
		if host = strings.TrimSpace(host); host != "" {
			if _, err := mgr.AddManual(ctx, host); err != nil {
				log.Warn("apparaat uit HWLOCAL_DEVICES niet toegevoegd", "host", host, "err", err)
			}
		}
	}
	go rec.Run(ctx, mgr.IDs)

	// generic read-only plugins (e.g. MHI air conditioners), configured in
	// the PWA and polled alongside the energy core
	if u := os.Getenv("HWLOCAL_TADO_URL"); u != "" {
		tado.UseBaseURL(u) // the fake tado of hwsim
		log.Info("tado plugin uses another tado", "url", u)
	}
	plugins := plugin.NewManager(st, hub, log, dataDir)
	plugins.Retention = time.Duration(cfg.retentionDays) * 24 * time.Hour
	plugins.Outages = outages
	plugins.Notices = notices
	// heating via mijn.ista.nl: a plugin type for the account, and a
	// service that fetches once a day (see docs/ISTA.md)
	istaSvc := &ista.Service{Store: st, Log: log, Loc: loc, BaseURL: os.Getenv("HWLOCAL_ISTA_URL"), Forecast: forecastSettings}
	if d, err := time.ParseDuration(os.Getenv("HWLOCAL_ISTA_PACE")); err == nil {
		istaSvc.Pace = d // for the simulator: "-1ns" fetches without pauses
	}
	istaSvc.Notices = notices
	istaSvc.OnData = func(cuid string) {
		hub.Publish(gateway.Event{Type: "ista", Data: map[string]string{"cuid": cuid}})
		plugins.PollType(ctx, ista.PluginType)
	}
	plugin.Register(ista.Provider{Svc: istaSvc})
	// the Plug-In Batteries a paired P1 meter controls
	plugin.Register(hwbattery.Provider{Meters: mgr})
	if err := plugins.Load(ctx); err != nil {
		return fmt.Errorf("plugins: %w", err)
	}
	go plugins.Run(ctx)
	go istaSvc.Run(ctx)

	tokenFromEnv := cfg.entsoeToken != ""
	if !tokenFromEnv {
		// the Windows service gets no environment variables; its token is
		// a file in the (protected) data directory
		path := filepath.Join(dataDir, entsoeTokenFile)
		token, err := readTokenFile(path)
		if err != nil {
			log.Warn("ENTSO-E-token niet gelezen", "file", path, "err", err)
		} else if token != "" {
			cfg.entsoeToken = token
			log.Info("ENTSO-E-token gelezen", "file", path)
		}
	}
	// a damaged auth.json must not open the gateway: refuse to start
	login, err := auth.Open(filepath.Join(dataDir, authFile))
	if err != nil {
		return fmt.Errorf("beveiliging: %w (herstel het bestand, of zet het wachtwoord uit met 'hwlocal password -off')", err)
	}
	if login.Enabled() {
		log.Info("wachtwoord staat aan: de app en API vragen om in te loggen")
	}
	access, err := server.LoadAccessSettings(filepath.Join(dataDir, accessFile))
	if err != nil {
		log.Warn("instellingen voor toegang op afstand niet gelezen; staan uit", "err", err)
	}
	tabOrder, err := server.LoadTabOrder(filepath.Join(dataDir, tabOrderFile))
	if err != nil {
		log.Warn("volgorde van de tabbladen niet gelezen; standaardvolgorde", "err", err)
	}
	sources, err := priceSources(cfg.priceSources, cfg.entsoeToken)
	if err != nil {
		return err
	}
	gas, err := gasSources(cfg.gasSources)
	if err != nil {
		return err
	}
	since, err := backfillFrom(cfg.priceBackfill)
	if err != nil {
		return err
	}
	pricesSaved := func(from time.Time) { calc.Invalidate("", from) }
	priceSvc := &prices.Service{Store: st, Commodity: prices.Electricity, Sources: sources, Log: log, BackfillFrom: since, OnSaved: pricesSaved}
	go priceSvc.Run(ctx)
	gasSvc := &prices.Service{Store: st, Commodity: prices.Gas, Sources: gas, Log: log, BackfillFrom: since, OnSaved: pricesSaved}
	go gasSvc.Run(ctx)
	if cfg.discovery {
		go discovery.Run(ctx, log, cfg.discoveryEvery, mgr.AddDiscovered)
	}

	// alert rules, checked against the live readings, with Web Push to the
	// PWA installs that turned alerts on
	vapidKey, err := webpush.LoadOrCreateKey(filepath.Join(dataDir, vapidKeyFile))
	if err != nil {
		return fmt.Errorf("sleutel voor pushmeldingen: %w", err)
	}
	push := &rules.Push{Sender: &webpush.Sender{Key: vapidKey, Subject: cfg.pushContact}, Store: st, Log: log}
	// every command to a device, from the app and from automations, is
	// checked and tried again; the latest command for a control wins
	ctl := &control.Service{
		Devices: mgr, Plugins: plugins, Log: log, Notices: notices,
		Publish: func(s control.Status) { hub.Publish(gateway.Event{Type: "control", Data: s}) },
	}
	defer ctl.Close()
	ruleEngine := &rules.Engine{
		Store: st, Devices: mgr, Plugins: plugins, Control: ctl, Loc: loc, Notify: push.Notify,
		Log: log, Interval: cfg.interval,
		// prices and all-in contract prices for conditions on the price
		Price: rulePrices{power: priceSvc, gas: gasSvc, calc: calc, loc: loc}.metric,
	}
	if err := ruleEngine.Load(ctx); err != nil {
		return fmt.Errorf("regels: %w", err)
	}
	go ruleEngine.Run(ctx)

	// PDF reports, and the scheduled ones mailed through the user's SMTP
	// server
	reports := &report.Scheduler{
		Store: st, Sender: &mail.Sender{}, Log: log, Notices: notices,
		Builder: &report.Builder{
			Store: st, Costs: calc, WaterCosts: waterCalc, Scenarios: &costs.Scenarios{Store: st}, Ista: istaSvc,
			Loc: loc, Version: version, Devices: reportDevices(mgr),
		},
	}
	go reports.Run(ctx)

	// a daily backup of the database and the files beside it
	backups, err := newBackups(cfg, dataDir, st, loc, log, push, notices)
	if err != nil {
		return err
	}
	go backups.Run(ctx)

	srv := &server.Server{
		Reports:        reports,
		Backups:        backups,
		Rules:          ruleEngine,
		Push:           push,
		Prices:         priceSvc,
		GasPrices:      gasSvc,
		Contracts:      contracts,
		Costs:          calc,
		WaterContracts: waterContracts,
		WaterCosts:     waterCalc,
		Scenarios:      &costs.Scenarios{Store: st},
		Forecast:       forecastSettings,
		Entsoe: &entsoeSetting{
			path: filepath.Join(dataDir, entsoeTokenFile), names: cfg.priceSources,
			fromEnv: tokenFromEnv, token: cfg.entsoeToken, svc: priceSvc, log: log,
		},
		Manager: mgr, Recorder: rec, Hub: hub, Plugins: plugins, Control: ctl, Tiles: st, Outages: st, Notices: st, Log: log, Web: web.Files(),
		Auth:           login,
		Ista:           istaSvc,
		AllowPublic:    cfg.allowPublic,
		AllowTailscale: cfg.allowTailscale,
		AllowedHosts:   allowedHosts(cfg),
		Access:         access,
		TabOrder:       tabOrder,
		Info: server.Info{
			Version: version, IntervalMS: cfg.interval.Milliseconds(), TLS: cfg.tlsMode,
			Discovery: cfg.discovery, TimeZone: loc.String(),
		},
	}

	var tlsCfg *tls.Config
	switch cfg.tlsMode {
	case "off":
	case "acme":
		log.Info("certificaat ophalen bij Let's Encrypt", "domain", cfg.domain, "staging", cfg.acmeStaging)
		tlsCfg, err = tlsconfig.ACME(ctx, tlsconfig.ACMEConfig{
			Domain: cfg.domain, Email: cfg.acmeEmail, CloudflareAPIToken: cfg.cfToken,
			Staging: cfg.acmeStaging, DataDir: dataDir,
		})
		if err != nil {
			return fmt.Errorf("Let's Encrypt: %w", err)
		}
	case "selfsigned":
		var caPEM []byte
		tlsCfg, caPEM, err = tlsconfig.SelfSigned(dataDir, strings.Split(cfg.tlsNames, ","))
		if err != nil {
			return fmt.Errorf("selfsigned certificaat: %w", err)
		}
		srv.CACert = caPEM
		srv.Info.CADownload = true
	default:
		return fmt.Errorf("onbekende TLS-modus %q (off, acme, selfsigned)", cfg.tlsMode)
	}

	// tailscale serve forwards to this gateway's own listener
	if tlsCfg != nil {
		srv.ServeTarget = serveTarget(cfg.httpsAddr, true)
	} else if cfg.httpAddr != "" {
		srv.ServeTarget = serveTarget(cfg.httpAddr, false)
	}
	srv.Serve = &tsserve.CLI{}

	handler := srv.Handler()
	var servers []*http.Server
	errc := make(chan error, 2)
	listen := func(s *http.Server, useTLS bool) {
		servers = append(servers, s)
		go func() {
			var err error
			if useTLS {
				err = s.ListenAndServeTLS("", "")
			} else {
				err = s.ListenAndServe()
			}
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- fmt.Errorf("%s: %w", s.Addr, err)
			}
		}()
	}
	newServer := func(addr string, h http.Handler) *http.Server {
		return &http.Server{
			Addr: addr, Handler: h,
			// a panic in a handler is recovered by net/http and reported
			// here; without ErrorLog it would go to stderr, which a Windows
			// service does not have
			ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelError),
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       2 * time.Minute,
			BaseContext:       func(net.Listener) context.Context { return ctx },
		}
	}
	if tlsCfg != nil {
		s := newServer(cfg.httpsAddr, handler)
		s.TLSConfig = tlsCfg
		listen(s, true)
		log.Info("PWA beschikbaar", "url", publicURL(cfg))
	}
	if cfg.httpAddr != "" {
		h := handler
		if tlsCfg != nil {
			h = redirectToHTTPS(cfg, handler)
		}
		listen(newServer(cfg.httpAddr, h), false)
		if tlsCfg == nil {
			log.Info("PWA beschikbaar", "url", "http://"+displayAddr(cfg.httpAddr))
		}
	}
	if len(servers) == 0 {
		return errors.New("geen HTTP- of HTTPS-adres ingesteld")
	}
	log.Info("gateway gestart", "version", version, "interval", cfg.interval, "tls", cfg.tlsMode, "data", dataDir)

	select {
	case <-ctx.Done():
	case err := <-errc:
		stop()
		return err
	}
	log.Info("afsluiten")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, s := range servers {
		_ = s.Shutdown(shutdownCtx)
	}
	rec.Flush(shutdownCtx)
	outages.Stop(shutdownCtx)
	return nil
}

// redirectToHTTPS sends browsers to HTTPS but keeps /ca.crt reachable over
// HTTP, so the local CA can be downloaded before it is trusted.
func redirectToHTTPS(cfg config, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ca.crt" {
			next.ServeHTTP(w, r)
			return
		}
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if cfg.domain != "" {
			host = cfg.domain
		}
		_, port, _ := net.SplitHostPort(cfg.httpsAddr)
		if port != "" && port != "443" {
			host = net.JoinHostPort(host, port)
		}
		http.Redirect(w, r, "https://"+host+r.URL.RequestURI(), http.StatusTemporaryRedirect)
	})
}

func publicURL(cfg config) string {
	host := cfg.domain
	if host == "" {
		host = displayAddr(cfg.httpsAddr)
	} else if _, port, _ := net.SplitHostPort(cfg.httpsAddr); port != "" && port != "443" {
		host = net.JoinHostPort(host, port)
	}
	return "https://" + host
}

// serveTarget is the address `tailscale serve` forwards to: the listener on
// loopback, or on the address it is bound to. With TLS the gateway's own
// certificate is not for 127.0.0.1, hence https+insecure (the hop stays on
// this machine).
func serveTarget(addr string, useTLS bool) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	if useTLS {
		return "https+insecure://" + net.JoinHostPort(host, port)
	}
	return "http://" + net.JoinHostPort(host, port)
}

func displayAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "<ip-van-deze-computer>"
	}
	return net.JoinHostPort(host, port)
}
