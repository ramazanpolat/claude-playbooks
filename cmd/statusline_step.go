package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

// The status line step (SPEC.md, "Status line and model"): a launch whose
// playbook has a status line of its own puts this step in front of it, in
// the launch-only --settings:
//
//	'<cpb>' __statusline --root '<root>' --launch <launch id> -- '<original command>'
//
// The step reads the JSON Claude Code pipes in, records what it says about
// usage (usage_log.go), adds one key, cpb, to the object, and execs the
// original under /bin/sh -c, as Claude Code would have run it, with the
// result on its standard input. The original is the same process from then
// on: its output, its exit code, Claude Code's timeout and any signal are
// as without the step. Whatever fails before that (no launch record, input
// that is not an object, an unwritable directory) costs only the record or
// the key: the original still runs, with the input as it came.

const statuslineStepArg = "__statusline"

// statuslineBlockVersion is the cpb key's shape, its v.
const statuslineBlockVersion = 1

// stepNow is the step's clock; a variable for tests.
var stepNow = time.Now

// stepExec replaces the step with the original; a variable for tests.
var stepExec = func(argv0 string, argv, env []string) error { return syscall.Exec(argv0, argv, env) }

// stepShell is what runs the original: /bin/sh, as Claude Code runs a
// status line command (seen on 2.1.322 on macOS).
const stepShell = "/bin/sh"

// stepArgs is what follows the step's hidden first argument.
type stepArgs struct {
	root, launch, original string
}

func parseStepArgs(args []string) (stepArgs, bool) {
	var a stepArgs
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--root":
			if i+1 >= len(args) {
				return a, false
			}
			i++
			a.root = args[i]
		case "--launch":
			if i+1 >= len(args) {
				return a, false
			}
			i++
			a.launch = args[i]
		case "--":
			if i+1 != len(args)-1 {
				return a, false
			}
			a.original = args[i+1]
			return a, true
		default:
			return a, false
		}
	}
	return a, false
}

// runStatuslineStep is the step. It returns only when the exec fails; a
// command line it cannot read still runs the last argument, the original,
// when there is one.
func runStatuslineStep(args []string, stdin io.Reader) error {
	in, rerr := io.ReadAll(stdin)
	a, ok := parseStepArgs(args)
	if !ok {
		if len(args) == 0 {
			return errors.New("the status line step needs its command")
		}
		a.original = args[len(args)-1]
	}
	out := in
	if ok && rerr == nil {
		out = stepRecord(a, in)
	}
	return execOriginal(a.original, out)
}

// stepRecord records the snapshot and returns the input with the cpb key
// added, or the input itself when it cannot be.
func stepRecord(a stepArgs, in []byte) []byte {
	if a.root == "" || !filepath.IsAbs(a.root) {
		return in
	}
	rec, ok := readLaunchRecordByID(a.root, a.launch)
	if !ok {
		return in
	}
	now := stepNow()
	if len(in) <= 256<<10 {
		if s, ok := snapshotOf(in); ok {
			s.At = rfc3339(now)
			s.ConfigDir, s.Playbook, s.Account, s.Key, s.Identity = rec.ConfigDir, rec.Playbook, rec.Account, rec.Key, rec.Identity
			if appendSnapshot(a.root, s, now) {
				writeLatest(a.root, s)
			} else if len(s.RateLimits) > 0 {
				// A render the write rule skipped still says the figure is
				// current.
				writeLatest(a.root, s)
			}
		}
	}
	block, err := statuslineBlock(a.root, rec, now)
	if err != nil {
		return in
	}
	return withCpbKey(in, block)
}

// statuslineBlockJSON is the cpb key the step adds (SPEC.md, "Status line
// and model"): names only, never an identity.
type statuslineBlockJSON struct {
	V       int               `json:"v"`
	Account *blockAccountJSON `json:"account"`
	Usage   []usageBlockRow   `json:"usage"`
}

type blockAccountJSON struct {
	Name string `json:"name"`
}

// statuslineBlockMax is how many usage entries the block carries.
const statuslineBlockMax = 16

// statuslineBlock is the cpb key for a session of rec: the name of its
// account (the name of its own usage entry), and the latest figure for
// every allowance cpb knows of, its own first, from cpb's own files only.
func statuslineBlock(root string, rec launchRecord, now time.Time) ([]byte, error) {
	key := rec.Key
	if key == "" {
		key = rec.Account
	}
	rows := usageRows(root, readLatestFigures(root), usageRowsOpts{now: now, selfKey: key})
	block := statuslineBlockJSON{V: statuslineBlockVersion, Usage: make([]usageBlockRow, 0, len(rows))}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].self && !rows[j].self })
	for _, r := range rows {
		if len(block.Usage) == statuslineBlockMax {
			break
		}
		b := r.block()
		if r.self && block.Account == nil {
			block.Account = &blockAccountJSON{Name: b.Account}
		}
		block.Usage = append(block.Usage, b)
	}
	return json.Marshal(block)
}

// withCpbKey adds "cpb": block as the last key of the JSON object in, and
// changes no other byte. Input that is not one JSON object, or that holds
// a cpb key already, comes back as it is.
func withCpbKey(in, block []byte) []byte {
	trimmed := bytes.TrimRight(in, " \t\r\n")
	start := bytes.TrimLeft(trimmed, " \t\r\n")
	if len(start) < 2 || start[0] != '{' || trimmed[len(trimmed)-1] != '}' || !json.Valid(trimmed) {
		return in
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(trimmed, &top) != nil {
		return in
	}
	if _, has := top["cpb"]; has {
		return in
	}
	end := len(trimmed) - 1
	var b bytes.Buffer
	b.Grow(len(in) + len(block) + 8)
	b.Write(trimmed[:end])
	if len(top) > 0 {
		b.WriteByte(',')
	}
	b.WriteString(`"cpb":`)
	b.Write(block)
	b.WriteByte('}')
	b.Write(in[len(trimmed):])
	if !json.Valid(bytes.TrimSpace(b.Bytes())) {
		return in
	}
	return b.Bytes()
}

// execOriginal replaces the step with /bin/sh -c original, data on its
// standard input: through a pipe when the pipe can hold it whole, else an
// unlinked temporary file, so the original reads all of it however slowly.
func execOriginal(original string, data []byte) error {
	if original == "" {
		return errors.New("the status line step has no command to run")
	}
	if fd, err := stdinFor(data); err == nil {
		if err := syscall.Dup2(fd, 0); err != nil {
			return err
		}
		if fd != 0 {
			_ = syscall.Close(fd)
		}
	}
	return stepExec(stepShell, []string{stepShell, "-c", original}, os.Environ())
}

// stepPipeBytes is what a pipe takes without a reader on every system cpb
// runs on (macOS starts a pipe at 16 KiB).
const stepPipeBytes = 8 << 10

// stdinFor is a descriptor that reads back data, then end of file.
func stdinFor(data []byte) (int, error) {
	if len(data) <= stepPipeBytes {
		r, w, err := os.Pipe()
		if err != nil {
			return -1, err
		}
		_, werr := w.Write(data)
		_ = w.Close()
		if werr != nil {
			_ = r.Close()
			return -1, werr
		}
		fd, err := syscall.Dup(int(r.Fd()))
		_ = r.Close()
		return fd, err
	}
	f, err := os.CreateTemp("", "cpb-statusline-*")
	if err != nil {
		return -1, err
	}
	_ = os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return -1, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		_ = f.Close()
		return -1, err
	}
	fd, err := syscall.Dup(int(f.Fd()))
	_ = f.Close()
	return fd, err
}
