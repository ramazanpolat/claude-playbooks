# Demo recording

`../demo.gif` is recorded with [VHS](https://github.com/charmbracelet/vhs)
against a disposable Docker container so paths look like a real machine
(`/home/you`, `/usr/local/bin`) and nothing touches the recording host.

Regenerate:

```bash
GOOS=linux GOARCH=arm64 go build -o cpb-linux .    # from the repo root
docker build -t cpb-demo docs/demo/                 # uses Dockerfile + claude-shim
vhs docs/demo/demo.tape                             # writes demo.gif
```

The `claude` inside the container is a shim that prints a banner — the
recording demonstrates cpb's own behavior (create, list, launcher dispatch),
not a live Claude session.
