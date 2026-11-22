package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	serviceName    = "hwlocal"
	serviceDisplay = "Home Energy Watch"
	serviceDesc    = "Lokaal energiedashboard voor HomeWizard Energy-apparaten (hwlocal)"
	firewallRule   = "hwlocal"
	logMaxBytes    = 10 << 20
)

// isService reports whether the process was started by the Windows service
// control manager.
func isService() bool {
	ok, err := svc.IsWindowsService()
	return err == nil && ok
}

// serviceDataDir is the default data directory of the service.
func serviceDataDir() string {
	base := os.Getenv("ProgramData")
	if base == "" {
		base = `C:\ProgramData`
	}
	return filepath.Join(base, "hwlocal")
}

// installDir is where "service install" puts the executable, so that the
// service account can read it wherever the download was.
func installDir() string {
	base := os.Getenv("ProgramFiles")
	if base == "" {
		base = `C:\Program Files`
	}
	return filepath.Join(base, "hwlocal")
}

// runService runs the gateway under the service control manager. The flags
// stored with the service arrive in os.Args; logs go to hwlocal.log in the
// data directory.
func runService() {
	_ = svc.Run(serviceName, &service{})
}

type service struct{}

func (*service) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	fs := flag.NewFlagSet(serviceName, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfg, err := parseConfig(fs, os.Args[1:], serviceDataDir())
	if err != nil {
		return true, 2
	}
	var out io.Writer = io.Discard
	if dir, err := filepath.Abs(cfg.dataDir); err == nil {
		if err := os.MkdirAll(dir, 0o700); err == nil {
			if lf, err := openLogFile(filepath.Join(dir, "hwlocal.log"), logMaxBytes); err == nil {
				defer lf.Close()
				out = lf
			}
		}
	}
	log := newLogger(cfg.logLevel, out)
	slog.SetDefault(log) // the log package's output (libraries) ends up in hwlocal.log too

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, cfg, log) }()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case err := <-done:
			if err != nil {
				log.Error("gestopt", "err", err)
				return true, 1 // a failure, so the recovery actions restart the service
			}
			return false, 0
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending, WaitHint: 15000}
				cancel()
				if err := <-done; err != nil {
					log.Error("gestopt", "err", err)
				}
				return false, 0
			}
		}
	}
}

const serviceUsage = `gebruik: hwlocal service <opdracht> [opties]

  install [vlaggen]  installeert of werkt bij: kopieert hwlocal.exe naar
                     %s, registreert de dienst
                     (automatisch starten), voegt een
                     firewallregel toe en start de dienst. Vlaggen zoals
                     -tls selfsigned worden bij de dienst bewaard; zonder
                     vlaggen blijven die van een bestaande installatie staan.
  uninstall          stopt en verwijdert de dienst en de firewallregel; de
                     gegevens in %s blijven staan
  start | stop       dienst starten of stoppen
  status             toont of de dienst draait

Vereist een opdrachtprompt als administrator (behalve status).
`

func serviceCommand(args []string) error {
	if len(args) == 0 {
		fmt.Printf(serviceUsage, installDir(), serviceDataDir())
		return nil
	}
	switch args[0] {
	case "install":
		return serviceInstall(args[1:])
	case "uninstall":
		return serviceUninstall()
	case "start":
		return withService(func(s *mgr.Service) error { return startService(s) })
	case "stop":
		return withService(func(s *mgr.Service) error { return stopService(s) })
	case "status":
		return serviceStatus()
	case "check-port":
		// used by the installer before it installs anything
		if len(args) < 2 {
			return errors.New("gebruik: hwlocal service check-port <adres> ...")
		}
		for _, addr := range args[1:] {
			if err := checkPort(addr); err != nil {
				return err
			}
		}
		return nil
	case "-h", "-help", "--help", "help":
		fmt.Printf(serviceUsage, installDir(), serviceDataDir())
		return nil
	}
	return fmt.Errorf("onbekende opdracht %q; zie 'hwlocal service'", args[0])
}

func connect() (*mgr.Mgr, error) {
	m, err := mgr.Connect()
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return nil, errors.New("geen toegang: start de opdrachtprompt als administrator")
	}
	return m, err
}

func withService(f func(*mgr.Service) error) error {
	m, err := connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("dienst %s is niet geïnstalleerd", serviceName)
	}
	defer s.Close()
	return f(s)
}

func serviceInstall(args []string) error {
	// validate the flags now rather than when the service starts
	fs := flag.NewFlagSet("hwlocal service install", flag.ContinueOnError)
	cfg, err := parseConfig(fs, args, serviceDataDir())
	if err != nil {
		return err
	}
	m, err := connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()

	exe := filepath.Join(installDir(), "hwlocal.exe")
	s, err := m.OpenService(serviceName)
	if err == nil {
		defer s.Close()
		old, err := s.Config()
		if err != nil {
			return err
		}
		if len(args) == 0 {
			if parts, err := windows.DecomposeCommandLine(old.BinaryPathName); err == nil && len(parts) > 1 {
				args = parts[1:]
				fs = flag.NewFlagSet("hwlocal", flag.ContinueOnError)
				if cfg, err = parseConfig(fs, args, serviceDataDir()); err != nil {
					return fmt.Errorf("bewaarde vlaggen: %w", err)
				}
				fmt.Println("bestaande vlaggen behouden:", strings.Join(args, " "))
			}
		}
		if err := stopService(s); err != nil {
			return err
		}
	}
	// with the old service stopped, the ports must be free; otherwise the
	// service would start and stop again right away
	if err := checkPorts(cfg); err != nil {
		return err
	}

	if err := installExecutable(exe); err != nil {
		return err
	}
	dataDir := serviceDataDir()
	custom := false
	fs.Visit(func(f *flag.Flag) { custom = custom || f.Name == "data" })
	if custom {
		if dataDir, err = filepath.Abs(cfg.dataDir); err != nil {
			return err
		}
	}
	if err := prepareDataDir(dataDir, custom); err != nil {
		return fmt.Errorf("datamap %s: %w", dataDir, err)
	}

	conf := mgr.Config{
		DisplayName:      serviceDisplay,
		Description:      serviceDesc,
		StartType:        mgr.StartAutomatic,
		DelayedAutoStart: true, // after the network is up
		ServiceStartName: `NT AUTHORITY\LocalService`,
		ErrorControl:     mgr.ErrorNormal,
	}
	if s == nil {
		s, err = m.CreateService(serviceName, exe, conf, args...)
		if err != nil {
			return fmt.Errorf("dienst aanmaken: %w", err)
		}
		defer s.Close()
	} else {
		conf.ServiceType = windows.SERVICE_WIN32_OWN_PROCESS
		conf.BinaryPathName = commandLine(exe, args)
		if err := s.UpdateConfig(conf); err != nil {
			return fmt.Errorf("dienst bijwerken: %w", err)
		}
	}
	restart := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 2 * time.Minute},
	}
	if err := s.SetRecoveryActions(restart, uint32((24 * time.Hour).Seconds())); err != nil {
		return fmt.Errorf("herstelacties: %w", err)
	}
	if err := s.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return fmt.Errorf("herstelacties: %w", err)
	}
	if err := addFirewallRule(exe); err != nil {
		fmt.Println("waarschuwing: firewallregel niet toegevoegd:", err)
	}
	if err := startService(s); err != nil {
		return err
	}
	fmt.Printf("dienst %s geïnstalleerd en gestart\n  programma: %s\n  gegevens:  %s (log: hwlocal.log)\n", serviceName, exe, dataDir)
	if cfg.httpAddr != "" {
		fmt.Println("  open http://localhost" + portOf(cfg.httpAddr))
	}
	return nil
}

func portOf(addr string) string {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[i:]
	}
	return ""
}

// commandLine builds a service command line the way mgr.CreateService does.
func commandLine(exe string, args []string) string {
	s := syscall.EscapeArg(exe)
	for _, a := range args {
		s += " " + syscall.EscapeArg(a)
	}
	return s
}

// installExecutable copies the running executable to exe, unless it is
// already running from there.
func installExecutable(exe string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return err
	}
	if strings.EqualFold(filepath.Clean(self), filepath.Clean(exe)) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		return err
	}
	src, err := os.Open(self)
	if err != nil {
		return err
	}
	defer src.Close()
	tmp := exe + ".new"
	dst, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		os.Remove(tmp)
		return err
	}
	if err := dst.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("%s vervangen: %w", exe, err)
	}
	return nil
}

// prepareDataDir creates the data directory and lets the service account
// (LocalService) write to it. The default directory gets its own ACL so that
// other users cannot read the device tokens; a directory given with -data
// only gets an extra entry for LocalService.
func prepareDataDir(dir string, custom bool) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if !custom {
		// SYSTEM and Administrators full control, LocalService modify;
		// protected, so nothing is inherited from ProgramData
		sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;0x1301bf;;;LS)")
		if err != nil {
			return err
		}
		dacl, _, err := sd.DACL()
		if err != nil {
			return err
		}
		return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
			windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
	}
	sid, err := windows.CreateWellKnownSid(windows.WinLocalServiceSid)
	if err != nil {
		return err
	}
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	old, _, err := sd.DACL()
	if err != nil {
		return err
	}
	dacl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: 0x1301bf, // modify
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}}, old)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

// addFirewallRule allows inbound traffic to the program (web server and
// mDNS) on private and domain networks. A service gets no "allow network
// access?" prompt, so without it other devices cannot reach the gateway.
func addFirewallRule(exe string) error {
	_ = netsh("delete", "rule", "name="+firewallRule)
	return netsh("add", "rule", "name="+firewallRule, "dir=in", "action=allow",
		"program="+exe, "enable=yes", "profile=private,domain")
}

func netsh(args ...string) error {
	out, err := exec.Command("netsh", append([]string{"advfirewall", "firewall"}, args...)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func startService(s *mgr.Service) error {
	st, err := s.Query()
	if err != nil {
		return err
	}
	if st.State == svc.Running {
		return nil
	}
	if err := s.Start(); err != nil {
		return fmt.Errorf("dienst starten: %w", err)
	}
	return waitFor(s, svc.Running)
}

func stopService(s *mgr.Service) error {
	st, err := s.Query()
	if err != nil {
		return err
	}
	if st.State == svc.Stopped {
		return nil
	}
	if st.State != svc.StopPending {
		if _, err := s.Control(svc.Stop); err != nil {
			return fmt.Errorf("dienst stoppen: %w", err)
		}
	}
	return waitFor(s, svc.Stopped)
}

func waitFor(s *mgr.Service, want svc.State) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State == want {
			return nil
		}
		if want == svc.Running && st.State == svc.Stopped {
			return fmt.Errorf("dienst is direct gestopt (exitcode %d); zie hwlocal.log in de datamap", st.ServiceSpecificExitCode)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("dienst reageert niet (status %d)", st.State)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func serviceUninstall() error {
	m, err := connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("dienst %s is niet geïnstalleerd", serviceName)
	}
	defer s.Close()
	if err := stopService(s); err != nil {
		return err
	}
	if err := s.Delete(); err != nil {
		return fmt.Errorf("dienst verwijderen: %w", err)
	}
	_ = netsh("delete", "rule", "name="+firewallRule)
	fmt.Printf("dienst %s verwijderd\n  het programma in %s en de gegevens in %s zijn blijven staan\n", serviceName, installDir(), serviceDataDir())
	return nil
}

func serviceStatus() error {
	// connect with read access only, so this works without administrator
	mh, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return err
	}
	m := &mgr.Mgr{Handle: mh}
	defer m.Disconnect()
	h, err := windows.OpenService(m.Handle, windows.StringToUTF16Ptr(serviceName), windows.SERVICE_QUERY_STATUS|windows.SERVICE_QUERY_CONFIG)
	if err != nil {
		fmt.Println("dienst hwlocal is niet geïnstalleerd")
		return nil
	}
	s := &mgr.Service{Name: serviceName, Handle: h}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return err
	}
	names := map[svc.State]string{
		svc.Stopped: "gestopt", svc.StartPending: "wordt gestart", svc.StopPending: "wordt gestopt",
		svc.Running: "actief", svc.Paused: "gepauzeerd",
	}
	state := names[st.State]
	if state == "" {
		state = fmt.Sprint(st.State)
	}
	fmt.Println("dienst hwlocal:", state)
	if conf, err := s.Config(); err == nil {
		fmt.Println("  opdracht:", conf.BinaryPathName)
	}
	return nil
}
