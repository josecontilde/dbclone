package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	titleSt = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7DD3FC"))
	dimSt   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	okSt    = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	errSt   = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	warnSt  = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	selSt   = lipgloss.NewStyle().Foreground(lipgloss.Color("#7DD3FC")).Bold(true)
	boxSt   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#7DD3FC")).Padding(1, 2)
)

type stage int

const (
	stURL stage = iota
	stProbe
	stMode
	stNewForm
	stPick
	stReplaceDB
	stReplaceConfirm
	stRun
	stDone
)

type (
	probeMsg struct {
		major int
		err   error
	}
	logMsg  string
	doneMsg struct {
		res *Result
		err error
	}
	containersMsg struct {
		cs  []container
		err error
	}
)

type model struct {
	st     stage
	rem    remote
	major  int
	err    string
	warn   string
	cursor int
	sp     spinner.Model
	w, h   int

	url    textinput.Model
	form   []textinput.Model // new: name, port, pass
	focus  int
	dbIn   textinput.Model
	confIn textinput.Model

	cs     []container
	target container

	ch   chan tea.Msg
	logs []string
	res  *Result
	rerr error
}

func input(prompt, val string, width int) textinput.Model {
	t := textinput.New()
	t.Prompt = prompt
	t.SetValue(val)
	t.Width = width
	t.CharLimit = 512
	return t
}

func newModel(rawURL string) model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = selSt
	u := input("URL › ", rawURL, 70)
	u.Focus()
	return model{sp: sp, url: u, confIn: input("› ", "", 30)}
}

func (m model) Init() tea.Cmd { return tea.Batch(textinput.Blink, m.sp.Tick) }

// ---------- comandos ----------

func probeCmd(r remote) tea.Cmd {
	return func() tea.Msg {
		v, err := r.majorVersion()
		return probeMsg{v, err}
	}
}

func containersCmd() tea.Cmd {
	return func() tea.Msg {
		cs, err := listPostgres()
		return containersMsg{cs, err}
	}
}

// lanza el trabajo en una goroutine; los logs y el resultado llegan por m.ch.
func (m *model) start(work func(log logFn) (*Result, error)) tea.Cmd {
	ch := make(chan tea.Msg, 128)
	m.ch, m.logs, m.st = ch, nil, stRun
	go func() {
		res, err := work(func(s string) { ch <- logMsg(s) })
		ch <- doneMsg{res, err}
	}()
	return m.wait()
}

func (m model) wait() tea.Cmd {
	ch := m.ch
	return func() tea.Msg { return <-ch }
}

// ---------- update ----------

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m, nil
	case spinner.TickMsg:
		var c tea.Cmd
		m.sp, c = m.sp.Update(msg)
		return m, c
	case probeMsg:
		if msg.err != nil {
			m.err, m.st = msg.err.Error(), stURL
			return m, textinput.Blink
		}
		m.major, m.st, m.cursor, m.err = msg.major, stMode, 0, ""
		return m, nil
	case containersMsg:
		if msg.err != nil {
			m.err, m.st = msg.err.Error(), stMode
			return m, nil
		}
		if len(msg.cs) == 0 {
			m.err, m.st = "no hay contenedores postgres locales; usa «nuevo»", stMode
			return m, nil
		}
		m.cs, m.st, m.cursor, m.err = msg.cs, stPick, 0, ""
		return m, nil
	case logMsg:
		m.logs = append(m.logs, string(msg))
		return m, m.wait()
	case doneMsg:
		m.res, m.rerr, m.st = msg.res, msg.err, stDone
		return m, nil
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		return m.key(msg)
	}
	return m, nil
}

func (m model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	switch m.st {

	case stURL:
		if s == "enter" {
			r, err := parseURL(m.url.Value())
			if err != nil {
				m.err = err.Error()
				return m, nil
			}
			m.rem, m.err, m.st = r, "", stProbe
			return m, probeCmd(r)
		}
		if s == "esc" {
			return m, tea.Quit
		}
		var c tea.Cmd
		m.url, c = m.url.Update(k)
		return m, c

	case stProbe:
		if s == "esc" {
			m.st = stURL
		}
		return m, nil

	case stMode:
		switch s {
		case "up", "k":
			m.cursor = (m.cursor + 1) % 2
		case "down", "j":
			m.cursor = (m.cursor + 1) % 2
		case "esc":
			m.st, m.err = stURL, ""
			return m, textinput.Blink
		case "q":
			return m, tea.Quit
		case "enter":
			m.err = ""
			if m.cursor == 1 {
				return m, containersCmd()
			}
			taken := allContainerNames()
			m.form = []textinput.Model{
				input("Nombre   › ", uniqueName("pg-"+shortName(m.rem.DB), taken), 40),
				input("Puerto   › ", strconv.Itoa(nextFreePort(5433)), 10),
				input("Password › ", "postgres", 30),
			}
			m.focus = 0
			m.form[0].Focus()
			m.st = stNewForm
			return m, textinput.Blink
		}
		return m, nil

	case stNewForm:
		switch s {
		case "esc":
			m.st, m.err = stMode, ""
			return m, nil
		case "tab", "down":
			m.form[m.focus].Blur()
			m.focus = (m.focus + 1) % len(m.form)
			m.form[m.focus].Focus()
			return m, nil
		case "shift+tab", "up":
			m.form[m.focus].Blur()
			m.focus = (m.focus + len(m.form) - 1) % len(m.form)
			m.form[m.focus].Focus()
			return m, nil
		case "enter":
			if m.focus < len(m.form)-1 {
				m.form[m.focus].Blur()
				m.focus++
				m.form[m.focus].Focus()
				return m, nil
			}
			p, _ := strconv.Atoi(strings.TrimSpace(m.form[1].Value()))
			o := NewOpts{Name: strings.TrimSpace(m.form[0].Value()), Port: p, Pass: m.form[2].Value()}
			if err := validateNew(o); err != nil {
				m.err = err.Error()
				return m, nil
			}
			m.err = ""
			r, major := m.rem, m.major
			return m, m.start(func(l logFn) (*Result, error) { return doNew(r, major, o, l) })
		}
		var c tea.Cmd
		m.form[m.focus], c = m.form[m.focus].Update(k)
		return m, c

	case stPick:
		switch s {
		case "up", "k":
			m.cursor = (m.cursor + len(m.cs) - 1) % len(m.cs)
		case "down", "j":
			m.cursor = (m.cursor + 1) % len(m.cs)
		case "esc":
			m.st = stMode
		case "enter":
			m.target = m.cs[m.cursor]
			m.warn = versionWarning(m.target, m.major)
			m.dbIn = input("Base destino › ", m.rem.DB, 40)
			m.dbIn.Focus()
			m.st = stReplaceDB
			return m, textinput.Blink
		}
		return m, nil

	case stReplaceDB:
		switch s {
		case "esc":
			m.st = stPick
			return m, nil
		case "enter":
			if strings.TrimSpace(m.dbIn.Value()) == "" {
				return m, nil
			}
			m.confIn = input("› ", "", 40)
			m.confIn.Focus()
			m.st = stReplaceConfirm
			return m, textinput.Blink
		}
		var c tea.Cmd
		m.dbIn, c = m.dbIn.Update(k)
		return m, c

	case stReplaceConfirm:
		switch s {
		case "esc":
			m.st = stReplaceDB
			return m, nil
		case "enter":
			if strings.TrimSpace(m.confIn.Value()) != m.target.Name {
				m.err = "el nombre no coincide"
				return m, nil
			}
			m.err = ""
			r, major, t, db := m.rem, m.major, m.target, strings.TrimSpace(m.dbIn.Value())
			return m, m.start(func(l logFn) (*Result, error) { return doReplace(r, major, t, db, l) })
		}
		var c tea.Cmd
		m.confIn, c = m.confIn.Update(k)
		return m, c

	case stDone:
		if s == "q" || s == "enter" || s == "esc" {
			return m, tea.Quit
		}
	}
	return m, nil
}

// ---------- view ----------

func (m model) View() string {
	var b strings.Builder
	b.WriteString(titleSt.Render("dbclone") + dimSt.Render("  · clona Postgres remoto → Docker local") + "\n\n")

	switch m.st {
	case stURL:
		b.WriteString("URL del Postgres remoto:\n\n" + m.url.View() + "\n")
		b.WriteString(dimSt.Render("\nenter conectar · esc salir"))

	case stProbe:
		b.WriteString(m.sp.View() + " conectando a " + m.rem.String() + "…")

	case stMode:
		b.WriteString(okSt.Render("✓ ") + m.rem.String() + dimSt.Render(fmt.Sprintf("  (PostgreSQL %d)", m.major)) + "\n\n¿Qué quieres hacer?\n\n")
		b.WriteString(menuLine(m.cursor == 0, "Crear un contenedor nuevo", "nombre y puerto sin colisiones"))
		b.WriteString(menuLine(m.cursor == 1, "Reemplazar un contenedor existente", "DROP + CREATE de la base y restore"))
		b.WriteString(dimSt.Render("\n↑/↓ mover · enter elegir · esc atrás · q salir"))

	case stNewForm:
		b.WriteString("Nuevo contenedor " + dimSt.Render(fmt.Sprintf("(postgres:%d, base %s, volumen <nombre>-data)", m.major, m.rem.DB)) + "\n\n")
		for _, f := range m.form {
			b.WriteString(f.View() + "\n")
		}
		b.WriteString(dimSt.Render("\ntab/↑↓ cambiar campo · enter en el último = crear · esc atrás"))

	case stPick:
		b.WriteString("Elige el contenedor a reemplazar:\n\n")
		for i, c := range m.cs {
			p := c.HostPort
			if p == "" {
				p = "-"
			}
			st := dimSt.Render(c.State)
			if c.State == "running" {
				st = okSt.Render(c.State)
			}
			line := fmt.Sprintf("%-28s puerto %-6s %s", c.Name, p, dimSt.Render(c.Image))
			b.WriteString(cur(i == m.cursor) + line + "  " + st + "\n")
		}
		b.WriteString(dimSt.Render("\n↑/↓ mover · enter elegir · esc atrás"))

	case stReplaceDB:
		b.WriteString("Contenedor: " + selSt.Render(m.target.Name) + "\n")
		if m.warn != "" {
			b.WriteString(warnSt.Render("⚠ "+m.warn) + "\n")
		}
		b.WriteString("\n" + m.dbIn.View() + dimSt.Render("\n\nenter continuar · esc atrás"))

	case stReplaceConfirm:
		b.WriteString(warnSt.Render(fmt.Sprintf("⚠ Se ELIMINARÁ la base '%s' en '%s' y se recreará desde %s.",
			strings.TrimSpace(m.dbIn.Value()), m.target.Name, m.rem.DB)) + "\n\n")
		b.WriteString("Escribe " + selSt.Render(m.target.Name) + " para confirmar:\n\n" + m.confIn.View())
		b.WriteString(dimSt.Render("\n\nenter ejecutar · esc atrás"))

	case stRun:
		b.WriteString(m.sp.View() + " trabajando…\n\n")
		b.WriteString(tail(m.logs, 12))

	case stDone:
		if m.rerr != nil {
			b.WriteString(errSt.Render("✗ falló: "+m.rerr.Error()) + "\n\n")
			b.WriteString(tail(m.logs, 8))
		} else {
			r := m.res
			b.WriteString(okSt.Render(fmt.Sprintf("✓ listo — %s tablas en %s", r.Tables, r.DB)) + "\n\n")
			body := "contenedor  " + r.Name + "\n"
			if cs := r.ConnString(); cs != "" {
				body += "conexión    " + cs
			}
			b.WriteString(boxSt.Render(body) + "\n")
			if n := len(r.RestoreErrors); n > 0 {
				b.WriteString(warnSt.Render(fmt.Sprintf("\n⚠ %d mensajes durante el restore (extensiones/roles suelen ser normales):", n)) + "\n")
				b.WriteString(dimSt.Render(tail(r.RestoreErrors, 6)))
			}
		}
		b.WriteString(dimSt.Render("\nenter/q salir"))
	}

	if m.err != "" {
		b.WriteString("\n\n" + errSt.Render("✗ "+m.err))
	}
	return lipgloss.NewStyle().Padding(1, 2).Render(b.String())
}

func cur(sel bool) string {
	if sel {
		return selSt.Render("❯ ")
	}
	return "  "
}

func menuLine(sel bool, title, desc string) string {
	t := title
	if sel {
		t = selSt.Render(title)
	}
	return cur(sel) + t + "  " + dimSt.Render(desc) + "\n"
}

func tail(ss []string, n int) string {
	if len(ss) > n {
		ss = ss[len(ss)-n:]
	}
	out := make([]string, len(ss))
	for i, s := range ss {
		if len(s) > 110 {
			s = s[:110] + "…"
		}
		out[i] = dimSt.Render("• " + s)
	}
	return strings.Join(out, "\n")
}
