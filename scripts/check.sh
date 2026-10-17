#!/usr/bin/env bash
# Verificación completa antes de cada commit: formato, vet, staticcheck,
# tests con detector de carreras y vet cruzado para linux/darwin/windows.
set -euo pipefail
cd "$(dirname "$0")/.."

step() { printf '\n== %s\n' "$*"; }

step "gofmt"
unformatted=$(gofmt -l .)
if [[ -n $unformatted ]]; then
	printf 'sin formato:\n%s\n' "$unformatted" >&2
	exit 1
fi

step "go vet"
go vet ./...

step "staticcheck"
staticcheck=$(command -v staticcheck || true)
gobin=$(go env GOBIN)
gopath=$(go env GOPATH)
gobin=${gobin:-${gopath%%:*}/bin}
if [[ -z $staticcheck && -x $gobin/staticcheck ]]; then
	staticcheck=$gobin/staticcheck
fi
if [[ -n $staticcheck ]]; then
	"$staticcheck" ./...
elif [[ -n ${CI:-} ]]; then
	echo "staticcheck no instalado" >&2
	exit 1
else
	echo "AVISO: staticcheck no instalado (go install honnef.co/go/tools/cmd/staticcheck@latest)" >&2
fi

step "go test -race"
go test -race ./...

# go vet revisa los tipos con los build tags de cada plataforma; los errores
# de enlace de Windows los cubre el job de CI.
host=$(go env GOOS)
for goos in linux darwin windows; do
	[[ $goos == "$host" ]] && continue
	step "go vet (GOOS=$goos)"
	GOOS=$goos go vet ./...
done

printf '\nOK\n'
