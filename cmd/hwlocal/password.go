package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bytebanger-nl/homeenergywatch/internal/auth"
)

// runPassword implements "hwlocal password": set, change or remove the
// password of the app on the machine itself, e.g. when it was forgotten. A
// running gateway picks up the change at its next request.
func runPassword(args []string) error {
	fs := flag.NewFlagSet("password", flag.ExitOnError)
	dir := fs.String("data", defaultPasswordDataDir(), "datamap van de gateway (HWLOCAL_DATA_DIR)")
	off := fs.Bool("off", false, "wachtwoord uitzetten: de app is dan weer open voor iedereen op het netwerk")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "gebruik: hwlocal password [-data map] [-off]\n\nZonder -off vraagt het om een nieuw wachtwoord (twee keer). Elke browser moet daarna opnieuw inloggen; API-tokens blijven werken.")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	dataDir, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	if st, err := os.Stat(dataDir); err != nil || !st.IsDir() {
		return fmt.Errorf("datamap %s niet gevonden; geef hem op met -data", dataDir)
	}
	path := filepath.Join(dataDir, authFile)
	a, err := auth.Open(path)
	if err != nil {
		// a damaged file can only be replaced
		if !*off {
			return fmt.Errorf("%w; zet het wachtwoord eerst uit met -off", err)
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		fmt.Println("beschadigd bestand verwijderd; er is geen wachtwoord meer (ook de API-tokens zijn weg)")
		return nil
	}
	if *off {
		if !a.Enabled() {
			fmt.Println("er was geen wachtwoord ingesteld")
			return nil
		}
		if err := a.ClearPassword(); err != nil {
			return err
		}
		matchOwner(path, dataDir)
		fmt.Println("wachtwoord uitgezet: iedereen op het netwerk kan de app weer gebruiken")
		return nil
	}
	in := bufio.NewReader(os.Stdin)
	pw, err := readSecret(in, "Nieuw wachtwoord: ")
	if err != nil {
		return err
	}
	if err := auth.ValidPassword(pw); err != nil {
		return err
	}
	again, err := readSecret(in, "Nog een keer: ")
	if err != nil {
		return err
	}
	if pw != again {
		return errors.New("de wachtwoorden zijn niet gelijk")
	}
	if err := a.SetPassword(pw); err != nil {
		return err
	}
	matchOwner(path, dataDir)
	fmt.Println("wachtwoord ingesteld; log in de app opnieuw in")
	return nil
}

// readSecret reads one line, without echo when stdin is a terminal that
// supports it.
func readSecret(in *bufio.Reader, prompt string) (string, error) {
	fmt.Print(prompt)
	restore := hideInput()
	line, err := in.ReadString('\n')
	if restore != nil {
		restore()
		fmt.Println()
	}
	if err != nil && line == "" {
		return "", errors.New("geen wachtwoord gelezen")
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// defaultPasswordDataDir is HWLOCAL_DATA_DIR, else ./data, else (on Windows)
// the data directory of the service.
func defaultPasswordDataDir() string {
	if d := os.Getenv("HWLOCAL_DATA_DIR"); d != "" {
		return d
	}
	if _, err := os.Stat(filepath.Join("data", "hwlocal.db")); err == nil {
		return "data"
	}
	if d := serviceDefaultDataDir(); d != "" {
		return d
	}
	return "data"
}
