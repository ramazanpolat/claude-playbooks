# Demo recording

`../demo.gif` is recorded with [VHS](https://github.com/charmbracelet/vhs)
inside a disposable container: the image holds VHS and the machine it
records, so paths look like a real machine (`/home/you`, `/usr/local/bin`)
and nothing touches the recording host. It needs only Docker or Podman.

Regenerate, from the repo root:

```bash
# GOARCH must match the container's platform
arch=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
GOOS=linux GOARCH=$arch CGO_ENABLED=0 go build -o docs/demo/cpb-linux .
docker build -t cpb-demo docs/demo/                 # Dockerfile + claude-shim
docker run --rm -v "$PWD":/vhs cpb-demo docs/demo/demo.tape   # writes docs/demo.gif
rm docs/demo/cpb-linux
```

The `claude` inside the container is a shim that prints a banner: the
recording shows cpb's own behaviour (`CREATE PLAYBOOK`, `SHOW PLAYBOOKS`,
launcher dispatch), not a live Claude session.
