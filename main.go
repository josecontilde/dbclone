// dbclone: clona una base Postgres remota (por URL) a un contenedor Docker local.
//
//	dbclone                         TUI interactiva (Bubble Tea)
//	dbclone -mode new|replace ...   modo headless (con -yes no pregunta nada)
package main

import (
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "✗ "+format+"\n", a...)
	os.Exit(1)
}

func main() {
	urlFlag := flag.String("url", "", "URL del Postgres remoto (o env DB_URL)")
	mode := flag.String("mode", "", "new | replace (si se omite: TUI)")
	name := flag.String("name", "", "nombre del contenedor (new: nuevo; replace: existente)")
	port := flag.Int("port", 0, "puerto host (new). 0 = automático desde 5433")
	pass := flag.String("password", "postgres", "password del postgres local (new)")
	dbName := flag.String("db", "", "base destino (replace). Default: la del remoto")
	flag.Parse()

	if err := checkDocker(); err != nil {
		die("%v", err)
	}
	raw := *urlFlag
	if raw == "" {
		raw = os.Getenv("DB_URL")
	}

	if *mode == "" {
		if _, err := tea.NewProgram(newModel(raw), tea.WithAltScreen()).Run(); err != nil {
			die("%v", err)
		}
		return
	}

	// ---- headless ----
	r, err := parseURL(raw)
	if err != nil {
		die("%v", err)
	}
	log := func(s string) { fmt.Println("→", s) }
	major, err := r.majorVersion()
	if err != nil {
		die("%v", err)
	}
	var res *Result
	switch *mode {
	case "new":
		n := *name
		if n == "" {
			n = uniqueName("pg-"+shortName(r.DB), allContainerNames())
		}
		p := *port
		if p == 0 {
			p = nextFreePort(5433)
		}
		res, err = doNew(r, major, NewOpts{Name: n, Port: p, Pass: *pass}, log)
	case "replace":
		cs, lerr := listPostgres()
		if lerr != nil {
			die("%v", lerr)
		}
		var target *container
		for i := range cs {
			if cs[i].Name == *name {
				target = &cs[i]
			}
		}
		if target == nil {
			die("no existe un contenedor postgres llamado %q", *name)
		}
		db := *dbName
		if db == "" {
			db = r.DB
		}
		res, err = doReplace(r, major, *target, db, log)
	default:
		die("mode inválido: %s", *mode)
	}
	if res != nil {
		for _, e := range res.RestoreErrors {
			fmt.Fprintln(os.Stderr, "  ⚠", e)
		}
	}
	if err != nil {
		die("%v", err)
	}
	fmt.Printf("✓ listo — %s tablas en %s (%s)\n  %s\n", res.Tables, res.DB, res.Name, res.ConnString())
}
