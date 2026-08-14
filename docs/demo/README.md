# Demo recording

`../demo.gif` is recorded with [VHS](https://github.com/charmbracelet/vhs)
against a disposable Docker container so paths look like a real machine
(`/home/you`, `/usr/local/bin`) and nothing touches the recording host.

Regenerate:

```bash
# from the repo root; GOARCH must match your Docker host platform
arch=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
GOOS=linux GOARCH=$arch go build -o docs/demo/cpb-linux .
docker build -t cpb-demo docs/demo/    # uses Dockerfile + claude-shim
vhs docs/demo/demo.tape                # writes demo.gif
```

The `claude` inside the container is a shim that prints a banner — the
recording demonstrates cpb's own behavior (create, list, launcher dispatch),
not a live Claude session.
