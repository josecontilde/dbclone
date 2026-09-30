# dbclone

CLI con TUI ([Bubble Tea](https://github.com/charmbracelet/bubbletea)) para clonar una base **PostgreSQL remota** (por URL) a un **contenedor Docker local**.

- **Nuevo contenedor**: nombre y puerto sin colisiones con lo que ya tengas corriendo.
- **Reemplazar contenedor existente**: `DROP` + `CREATE` de la base y restore de DDL + datos.
- No necesitas `pg_dump` ni `psql` instalados: se usan desde contenedores `postgres:<versión del remoto>`.

## Requisitos

| Requisito | Notas |
|---|---|
| Docker | Docker Engine (Linux) o Docker Desktop (Windows/macOS), con el daemon corriendo |
| Go 1.22+ | Solo si compilas desde el código fuente |

## Instalación

### Linux

```bash
# 1. Docker (ejemplos)
#    Arch:          sudo pacman -S docker && sudo systemctl enable --now docker
#    Debian/Ubuntu: sudo apt install docker.io && sudo systemctl enable --now docker
sudo usermod -aG docker "$USER"   # cerrar sesión y volver a entrar

# 2. Go (ejemplos)
#    Arch:          sudo pacman -S go
#    Debian/Ubuntu: sudo apt install golang-go      (o https://go.dev/dl/)

# 3. Compilar e instalar
git clone https://github.com/josecontilde/dbclone.git
cd dbclone
go build -o dbclone .
sudo install -m 0755 dbclone /usr/local/bin/dbclone   # opcional, para usarlo desde cualquier lado
```

O directamente con Go:

```bash
go install github.com/josecontilde/dbclone@latest    # deja el binario en $(go env GOPATH)/bin
```

### Windows (PowerShell)

```powershell
# 1. Docker Desktop (con backend WSL2) y Go
winget install -e --id Docker.DockerDesktop
winget install -e --id GoLang.Go
winget install -e --id Git.Git
# Reinicia la terminal y abre Docker Desktop una vez para que arranque el daemon.

# 2. Compilar
git clone https://github.com/josecontilde/dbclone.git
cd dbclone
go build -o dbclone.exe .

# 3. Opcional: agregarlo al PATH
mkdir $HOME\bin -Force; Copy-Item dbclone.exe $HOME\bin\
[Environment]::SetEnvironmentVariable("Path", $env:Path + ";$HOME\bin", "User")
```

Usa **Windows Terminal** o PowerShell para que la TUI se vea bien (el `cmd.exe` clásico puede mostrar mal los colores).

### macOS

```bash
brew install go
brew install --cask docker     # abre Docker Desktop una vez
git clone https://github.com/josecontilde/dbclone.git && cd dbclone && go build -o dbclone .
```

### Compilar para otra plataforma

```bash
GOOS=windows GOARCH=amd64 go build -o dbclone.exe .
GOOS=linux   GOARCH=amd64 go build -o dbclone .
GOOS=darwin  GOARCH=arm64 go build -o dbclone .
```

## Uso

### TUI (recomendado)

```bash
# Linux/macOS
export DB_URL='postgresql://usuario:password@host:5432/nombre_db'
./dbclone
```

```powershell
# Windows
$env:DB_URL = 'postgresql://usuario:password@host:5432/nombre_db'
.\dbclone.exe
```

Si no defines `DB_URL`, la primera pantalla te la pide. Los caracteres especiales de la password van codificados en URL (`(` → `%28`, `[` → `%5B`, `+` → `%2B`).

| Tecla | Acción |
|---|---|
| `enter` | avanzar / elegir |
| `esc` | volver |
| `tab` / `↑` `↓` | cambiar de campo o de opción |
| `ctrl+c` | salir |

### Sin TUI (scripts)

```bash
./dbclone -mode new     -name pg-mi-db -port 5440 -password secreto
./dbclone -mode replace -name pg-mi-db
```

| Flag | Descripción |
|---|---|
| `-url` | URL remota (si no, usa `DB_URL`) |
| `-mode` | `new` o `replace`; si se omite se abre la TUI |
| `-name` | nombre del contenedor (nuevo, o existente en `replace`) |
| `-port` | puerto local para `new`; `0` = primer libre desde 5433 |
| `-password` | password del postgres local (`new`, default `postgres`) |
| `-db` | base destino en `replace` (default: la del remoto) |

## Cómo evita colisiones

- **Nombre**: se compara contra todos los contenedores (incluidos los detenidos); la TUI sugiere `pg-<db>`, `pg-<db>-2`, …
- **Puerto**: se descartan los puertos publicados por cualquier contenedor (aunque esté detenido) y los ocupados en el host; se sugiere el primero libre desde 5433.
- Cada contenedor nuevo usa su propio volumen `<nombre>-data` y la etiqueta `managed-by=dbclone`.

## Notas y límites

- El restore usa `pg_dump --no-owner --no-privileges` en formato plano; extensiones o roles no disponibles localmente se reportan pero no abortan el restore.
- En `replace`, si el contenedor tiene una versión de Postgres menor que la del remoto, se muestra una advertencia.
- Si el remoto es `localhost`/`127.0.0.1`: en Linux se usa red `host`; en Docker Desktop, `host.docker.internal`.
- Las credenciales viajan por variables de entorno (`PGPASSWORD`, …), no por argumentos, así que no aparecen en `ps`. No las guardes en el repo; `.env` está en `.gitignore`.
