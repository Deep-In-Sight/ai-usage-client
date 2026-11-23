# Sourced by the build scripts: checks VERSION and picks the go command.
# shellcheck shell=sh
check_version() {
  if ! printf '%s\n' "$1" | grep -Eqx '[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?' ||
     printf '%s\n' "$1" | grep -Eq '(^|[.-])$|\.\.|-\.|\.-'; then
    echo "bad version: $1 (want MAJOR.MINOR.PATCH[-pre])" >&2
    exit 2
  fi
}
# go_cmd: "go" when installed, else the docker wrapper.
go_cmd() {
  if command -v go >/dev/null 2>&1; then echo go; else echo tools/go.sh; fi
}
