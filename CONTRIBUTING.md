# Contributing

Thanks for your interest. Small, focused changes are easiest to review.

Scope: hdmi-matrix-web-controller is a standalone micro-service for HDMI matrix switchers, for local networks only. Changes that send data beyond the local network are out of scope.

## Before you start

- For bugs and ideas, open an issue first, especially for larger changes.
- Check that the change keeps the page working without a build step.
- Do not add services that require external accounts or network access beyond the matrices themselves.

## Development

```bash
cd hdmi-matrix-web-controller
cp .env.example .env
docker compose up -d --build     # http://127.0.0.1:8080
```

Syntax checks you can run locally:

```bash
node --check static/app.js
node --check static/i18n.js
node --check static/protocol.js
for f in static/lang/*.json; do node -e "JSON.parse(require('fs').readFileSync('$f','utf8'))"; done
```

Tests for the installer run without Docker or network: the tests replace `docker` and `curl` with stubs.

```bash
bash tests/install_test.sh
shellcheck install.sh tests/install_test.sh
```

Tests for the server (Go 1.24 or newer):

```bash
gofmt -l .
go vet ./...
go test -race ./...
```

## Pull requests

- Keep one logical change per pull request.
- Describe what changed and how you tested it, including the matrix model and firmware if you touched the protocol.
- New user-visible text goes into **all** files in `static/lang/`. English is the reference; a missing key falls back to English.
- Comments explain *why*, not *what*.

## Hardware-facing changes

Protocol changes must be verified on a real device before merging. If you cannot test on hardware, say so in the pull request.

## Code of conduct

Participation is governed by [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).
