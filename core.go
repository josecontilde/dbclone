package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const label = "managed-by=dbclone"

type logFn func(string)

// ---------- helpers ----------

func docker(args ...string) (string, error) {
	out, err := exec.Command("docker", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func dockerErr(out string, err error) error {
	if out == "" {
		return err
	}
	return fmt.Errorf("%s", out)
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func shortName(s string) string {
	return regexp.MustCompile(`[^a-zA-Z0-9_.-]+`).ReplaceAllString(s, "-")
}

func checkDocker() error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker no está en el PATH")
	}
	if out, err := docker("info", "--format", "{{.ServerVersion}}"); err != nil {
		return fmt.Errorf("el daemon de docker no responde: %s", out)
	}
	return nil
}

// ---------- remoto ----------

type remote struct {
	Host, Port, User, Pass, DB, SSLMode string
}

func parseURL(raw string) (remote, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return remote{}, fmt.Errorf("URL inválida")
	}
	r := remote{Host: u.Hostname(), Port: u.Port(), User: u.User.Username(), DB: strings.TrimPrefix(u.Path, "/"), SSLMode: u.Query().Get("sslmode")}
	r.Pass, _ = u.User.Password()
	if r.Port == "" {
		r.Port = "5432"
	}
	if r.DB == "" {
		return r, fmt.Errorf("la URL no trae nombre de base")
	}
	return r, nil
}

func (r remote) String() string { return fmt.Sprintf("%s@%s:%s/%s", r.User, r.Host, r.Port, r.DB) }

// credenciales por env (no aparecen en `ps`).
func (r remote) env() []string {
	e := append(os.Environ(), "PGHOST="+r.Host, "PGPORT="+r.Port, "PGUSER="+r.User, "PGPASSWORD="+r.Pass, "PGDATABASE="+r.DB)
	if r.SSLMode != "" {
		e = append(e, "PGSSLMODE="+r.SSLMode)
	}
	return e
}

var pgEnvFlags = []string{"-e", "PGHOST", "-e", "PGPORT", "-e", "PGUSER", "-e", "PGPASSWORD", "-e", "PGDATABASE", "-e", "PGSSLMODE"}

func (r remote) client(image string, cmdArgs ...string) *exec.Cmd {
	args := []string{"run", "--rm"}
	rr := r
	if h := r.Host; h == "localhost" || h == "127.0.0.1" || h == "::1" {
		// remoto en la propia máquina: en Linux basta host networking; en Docker Desktop (Win/Mac) se usa host.docker.internal.
		if runtime.GOOS == "linux" {
			args = append(args, "--network", "host")
		} else {
			rr.Host = "host.docker.internal"
		}
	}
	args = append(args, pgEnvFlags...)
	args = append(args, image)
	args = append(args, cmdArgs...)
	c := exec.Command("docker", args...)
	c.Env = rr.env()
	return c
}

func (r remote) majorVersion() (int, error) {
	out, err := r.client("postgres:17-alpine", "psql", "-Atc", "show server_version_num").CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("no pude conectar al remoto: %s", strings.TrimSpace(string(out)))
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("versión inesperada: %q", out)
	}
	return n / 10000, nil
}

// ---------- docker local ----------

type container struct {
	Name, Image, State, HostPort string
}

func listPostgres() ([]container, error) {
	out, err := docker("ps", "-a", "--format", "{{json .}}")
	if err != nil {
		return nil, dockerErr(out, err)
	}
	var res []container
	for _, l := range strings.Split(out, "\n") {
		if l == "" {
			continue
		}
		var j struct{ Names, Image, State, Ports string }
		if json.Unmarshal([]byte(l), &j) != nil {
			continue
		}
		img := strings.ToLower(j.Image)
		if !strings.Contains(img, "postgres") && !strings.Contains(img, "postgis") {
			continue
		}
		c := container{Name: j.Names, Image: j.Image, State: j.State}
		if m := regexp.MustCompile(`:(\d+)->5432`).FindStringSubmatch(j.Ports); m != nil {
			c.HostPort = m[1]
		}
		res = append(res, c)
	}
	sort.Slice(res, func(i, j int) bool { return res[i].Name < res[j].Name })
	return res, nil
}

func allContainerNames() map[string]bool {
	out, _ := docker("ps", "-a", "--format", "{{.Names}}")
	m := map[string]bool{}
	for _, n := range strings.Fields(out) {
		m[n] = true
	}
	return m
}

// puertos publicados por cualquier contenedor (incluso detenidos).
func dockerUsedPorts() map[int]bool {
	out, _ := docker("ps", "-a", "--format", "{{.Ports}}")
	m := map[int]bool{}
	for _, mm := range regexp.MustCompile(`:(\d+)->`).FindAllStringSubmatch(out, -1) {
		p, _ := strconv.Atoi(mm[1])
		m[p] = true
	}
	return m
}

func portFree(p int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", p))
	if err != nil {
		return false
	}
	l.Close()
	return true
}

func portAvailable(p int) bool { return !dockerUsedPorts()[p] && portFree(p) }

func nextFreePort(start int) int {
	used := dockerUsedPorts()
	for p := start; p < 65535; p++ {
		if !used[p] && portFree(p) {
			return p
		}
	}
	return start
}

func uniqueName(base string, taken map[string]bool) string {
	if !taken[base] {
		return base
	}
	for i := 2; ; i++ {
		if n := fmt.Sprintf("%s-%d", base, i); !taken[n] {
			return n
		}
	}
}

func inspectEnv(name, key string) string {
	out, _ := docker("inspect", "-f", "{{range .Config.Env}}{{println .}}{{end}}", name)
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, key+"=") {
			return strings.TrimPrefix(l, key+"=")
		}
	}
	return ""
}

func waitReady(name, user string, log logFn) error {
	log("esperando a postgres…")
	for i := 0; i < 60; i++ {
		if _, err := docker("exec", name, "pg_isready", "-U", user); err == nil {
			time.Sleep(2 * time.Second) // el init temporal también responde a pg_isready
			if _, err := docker("exec", name, "psql", "-U", user, "-d", "postgres", "-Atc", "select 1"); err == nil {
				return nil
			}
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("timeout esperando al contenedor %s", name)
}

// ---------- operaciones ----------

type Result struct {
	Name, User, Pass, DB, Port string
	Tables                     string
	RestoreErrors              []string
}

func psqlLocal(name, user, db, sql string) error {
	if out, err := docker("exec", name, "psql", "-U", user, "-d", db, "-v", "ON_ERROR_STOP=1", "-c", sql); err != nil {
		return dockerErr(out, err)
	}
	return nil
}

func dropAndCreate(name, user, db string, log logFn) error {
	log("recreando base " + db)
	if err := psqlLocal(name, user, "postgres", fmt.Sprintf(
		"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '%s' AND pid <> pg_backend_pid()", strings.ReplaceAll(db, "'", "''"))); err != nil {
		return err
	}
	if err := psqlLocal(name, user, "postgres", "DROP DATABASE IF EXISTS "+quoteIdent(db)); err != nil {
		return err
	}
	return psqlLocal(name, user, "postgres", "CREATE DATABASE "+quoteIdent(db))
}

func dumpAndRestore(r remote, major int, name, user, db string, log logFn) ([]string, error) {
	log(fmt.Sprintf("volcando %s (pg_dump %d) → %s/%s", r.DB, major, name, db))
	dump := r.client(fmt.Sprintf("postgres:%d-alpine", major), "pg_dump", "--no-owner", "--no-privileges", "--format=plain")
	// ON_ERROR_STOP=0: una extensión/rol no soportado no debe tumbar el restore.
	load := exec.Command("docker", "exec", "-i", name, "psql", "-q", "-U", user, "-d", db, "-v", "ON_ERROR_STOP=0")

	pipe, err := dump.StdoutPipe()
	if err != nil {
		return nil, err
	}
	load.Stdin = pipe
	dumpErr, _ := dump.StderrPipe()
	loadErr, _ := load.StderrPipe()

	var errs []string
	if err := load.Start(); err != nil {
		return nil, err
	}
	if err := dump.Start(); err != nil {
		return nil, err
	}
	lines := make(chan string, 64)
	done := make(chan struct{}, 2)
	fwd := func(rd io.Reader, prefix string) {
		sc := bufio.NewScanner(rd)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			lines <- prefix + sc.Text()
		}
		done <- struct{}{}
	}
	go fwd(dumpErr, "pg_dump: ")
	go fwd(loadErr, "psql: ")
	go func() {
		<-done
		<-done
		close(lines)
	}()
	for l := range lines {
		if len(errs) < 50 {
			errs = append(errs, l)
		}
	}
	if err := dump.Wait(); err != nil {
		return errs, fmt.Errorf("pg_dump falló: %v", err)
	}
	if err := load.Wait(); err != nil {
		return errs, fmt.Errorf("restore falló: %v", err)
	}
	return errs, nil
}

func countTables(name, user, db string) string {
	out, _ := docker("exec", name, "psql", "-U", user, "-d", db, "-Atc",
		"select count(*) from information_schema.tables where table_schema not in ('pg_catalog','information_schema')")
	return strings.TrimSpace(out)
}

type NewOpts struct {
	Name, Pass string
	Port       int
}

func validateNew(o NewOpts) error {
	if o.Name == "" || !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`).MatchString(o.Name) {
		return fmt.Errorf("nombre inválido")
	}
	if allContainerNames()[o.Name] {
		return fmt.Errorf("el nombre '%s' ya existe", o.Name)
	}
	if o.Port < 1 || o.Port > 65535 {
		return fmt.Errorf("puerto inválido")
	}
	if !portAvailable(o.Port) {
		return fmt.Errorf("el puerto %d está ocupado", o.Port)
	}
	return nil
}

func doNew(r remote, major int, o NewOpts, log logFn) (*Result, error) {
	if err := validateNew(o); err != nil {
		return nil, err
	}
	const user = "postgres"
	log(fmt.Sprintf("creando contenedor %s (postgres:%d) en puerto %d", o.Name, major, o.Port))
	if out, err := docker("run", "-d", "--name", o.Name, "--label", label,
		"-e", "POSTGRES_USER="+user, "-e", "POSTGRES_PASSWORD="+o.Pass, "-e", "POSTGRES_DB=postgres",
		"-p", fmt.Sprintf("%d:5432", o.Port), "-v", o.Name+"-data:/var/lib/postgresql/data",
		fmt.Sprintf("postgres:%d", major)); err != nil {
		return nil, dockerErr(out, err)
	}
	if err := waitReady(o.Name, user, log); err != nil {
		return nil, err
	}
	if err := dropAndCreate(o.Name, user, r.DB, log); err != nil {
		return nil, err
	}
	errs, err := dumpAndRestore(r, major, o.Name, user, r.DB, log)
	if err != nil {
		return nil, err
	}
	return &Result{Name: o.Name, User: user, Pass: o.Pass, DB: r.DB, Port: strconv.Itoa(o.Port),
		Tables: countTables(o.Name, user, r.DB), RestoreErrors: errs}, nil
}

func doReplace(r remote, major int, c container, db string, log logFn) (*Result, error) {
	if c.State != "running" {
		log("iniciando " + c.Name)
		if out, err := docker("start", c.Name); err != nil {
			return nil, dockerErr(out, err)
		}
	}
	user := inspectEnv(c.Name, "POSTGRES_USER")
	if user == "" {
		user = "postgres"
	}
	pass := inspectEnv(c.Name, "POSTGRES_PASSWORD")
	if err := waitReady(c.Name, user, log); err != nil {
		return nil, err
	}
	if err := dropAndCreate(c.Name, user, db, log); err != nil {
		return nil, err
	}
	errs, err := dumpAndRestore(r, major, c.Name, user, db, log)
	if err != nil {
		return nil, err
	}
	return &Result{Name: c.Name, User: user, Pass: pass, DB: db, Port: c.HostPort,
		Tables: countTables(c.Name, user, db), RestoreErrors: errs}, nil
}

// versión menor del contenedor respecto al remoto → advertencia.
func versionWarning(c container, major int) string {
	if m := regexp.MustCompile(`postgres:(\d+)`).FindStringSubmatch(c.Image); m != nil {
		if tm, _ := strconv.Atoi(m[1]); tm < major {
			return fmt.Sprintf("el contenedor es PG %d y el remoto PG %d: el restore puede fallar", tm, major)
		}
	}
	return ""
}

func (r *Result) ConnString() string {
	if r.Port == "" {
		return ""
	}
	return fmt.Sprintf("postgresql://%s:%s@localhost:%s/%s", r.User, url.QueryEscape(r.Pass), r.Port, url.PathEscape(r.DB))
}
