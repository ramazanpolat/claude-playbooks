package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ramazanpolat/claude-playbooks/internal/grammar"
)

// The resume guard (SPEC.md, "Sessions"): cpb run and
// the launchers hand --resume and --continue to claude as they are, after
// refusing a session that is still live in another process. Two processes
// on one session id is the --continue hazard, and it corrupts the session.
// Claude Code finds a session by the folder it ran in, so a --resume <id>
// recorded in another folder is refused with the cd that resumes it.

// claudeSessionID is a session id as Claude Code makes one: a UUID. A
// --resume value of any other shape is a search term for its picker.
var claudeSessionID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// resumeTarget is what claudeArgs ask claude to resume: the id of
// -r/--resume <id> or --resume=<id>, and whether -c/--continue is set. A
// --resume with no id, or a search term, opens Claude's picker, and
// --fork-session starts a new id, so neither has a target. The last
// --resume is the one claude takes.
func resumeTarget(claudeArgs []string) (id string, cont bool) {
	fork := false
scan:
	for i := 0; i < len(claudeArgs); i++ {
		switch a := claudeArgs[i]; {
		case a == "--":
			break scan
		case a == "-r" || a == "--resume":
			id = ""
			if i+1 < len(claudeArgs) && claudeSessionID.MatchString(claudeArgs[i+1]) {
				id = claudeArgs[i+1]
				i++
			}
		case strings.HasPrefix(a, "--resume="):
			id = ""
			if v := strings.TrimPrefix(a, "--resume="); claudeSessionID.MatchString(v) {
				id = v
			}
		case a == "-c" || a == "--continue":
			cont = true
		case a == "--fork-session":
			fork = true
		}
	}
	if fork {
		return "", false
	}
	return id, cont
}

// guardResume refuses a launch in d, from the working directory, whose
// claudeArgs resume a session still live in another process, or, for
// --resume <id>, one that ran in another folder. A session cpb finds no
// transcript of is left to claude, which says so itself.
func guardResume(d sessionDir, claudeArgs []string) error {
	id, cont := resumeTarget(claudeArgs)
	if id == "" && !cont {
		return nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	lives, err := liveByID()
	if err != nil {
		return err
	}
	if id != "" {
		if s := lives[id]; s != nil {
			return liveRefusal(id, s, d)
		}
		if !transcriptHere(d.path, cwd, id) {
			if p := transcriptPath(d.path, "", id); p != "" {
				if there := transcriptCwd(p); there != "" && !sameDir(there, cwd) {
					return fmt.Errorf("session %s ran in %s, and Claude Code resumes a session only from the folder it ran in: %s",
						id, there, d.resumeIn(id, there))
				}
			}
		}
	}
	if cont {
		if last := newestTranscript(d.path, cwd); last != "" {
			if s := lives[last]; s != nil {
				return fmt.Errorf("--continue resumes the newest session in this folder, and %w", liveRefusal(last, s, d))
			}
		}
	}
	return nil
}

// resumeNote is what a sandboxed launch says instead of guarding: the
// sessions live inside the sandbox, where cpb cannot see them.
func resumeNote(claudeArgs []string) {
	if id, cont := resumeTarget(claudeArgs); id != "" || cont {
		fmt.Fprintln(os.Stderr, "Note: the sandbox holds this playbook's sessions, so cpb cannot check that the one claude resumes is not running elsewhere")
	}
}

// transcriptHere reports whether id's transcript in configDir is under
// cwd's project directory, in any of its spellings.
func transcriptHere(configDir, cwd, id string) bool {
	for _, c := range cwdSpellings(cwd) {
		if fi, err := os.Stat(filepath.Join(configDir, "projects", encodeProjectDir(c), id+".jsonl")); err == nil && fi.Mode().IsRegular() {
			return true
		}
	}
	return false
}

// newestTranscript is the session --continue picks in configDir from cwd:
// the newest transcript under the folder's project directory, in any of its
// spellings. "" when there is none.
func newestTranscript(configDir, cwd string) string {
	var best string
	var bestT time.Time
	for _, c := range cwdSpellings(cwd) {
		m, _ := filepath.Glob(filepath.Join(configDir, "projects", encodeProjectDir(c), "*.jsonl"))
		for _, p := range m {
			id := strings.TrimSuffix(filepath.Base(p), ".jsonl")
			if !grammar.ValidSessionID(id) || strings.HasPrefix(id, "agent-") {
				continue
			}
			fi, err := os.Stat(p)
			if err != nil || !fi.Mode().IsRegular() {
				continue
			}
			if best == "" || fi.ModTime().After(bestT) {
				best, bestT = id, fi.ModTime()
			}
		}
	}
	return best
}

// sameDir reports whether a and b name one directory: by identity when
// both exist, else by their cleaned spelling.
func sameDir(a, b string) bool {
	ai, aerr := os.Stat(a)
	bi, berr := os.Stat(b)
	if aerr == nil && berr == nil {
		return os.SameFile(ai, bi)
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// liveByID indexes the live sessions of every config dir cpb knows by
// session id, whichever dir the launch binds: a transcript copied into
// another dir is still the same live session.
func liveByID() (map[string]*liveSession, error) {
	dirs, err := sessionDirs("")
	if err != nil {
		return nil, err
	}
	m := map[string]*liveSession{}
	for _, s := range readSessionFiles(dirs) {
		s := s
		m[s.f.SessionID] = &s
	}
	return m, nil
}

// cwdSpellings are the names of the working directory a session there may
// have been recorded under: as cpb sees it, as the shell spelled it ($PWD),
// and with symlinks resolved (/tmp and /private/tmp on macOS).
func cwdSpellings(cwd string) []string {
	out := []string{cwd}
	add := func(p string) {
		if p == "" {
			return
		}
		for _, o := range out {
			if o == p {
				return
			}
		}
		out = append(out, p)
	}
	if pwd := os.Getenv("PWD"); pwd != "" {
		if a, err := os.Stat(pwd); err == nil {
			if b, err := os.Stat(cwd); err == nil && os.SameFile(a, b) {
				add(pwd)
			}
		}
	}
	if r, err := filepath.EvalSymlinks(cwd); err == nil {
		add(r)
	}
	return out
}

// liveRefusal is the refusal for a session still live in another process,
// launched in d.
func liveRefusal(id string, s *liveSession, d sessionDir) error {
	pick := "close that one first, or pick another with " + d.pickCommand()
	if s.state == liveUnknown {
		why := fmt.Sprintf("pid %d in another pid domain, %q", s.f.PID, s.f.PIDDomain)
		if s.f.PIDDomain == "" || s.f.PIDDomain == pidDomain {
			why = fmt.Sprintf("pid %d is alive, and its session file records no start time to confirm it is the same process", s.f.PID)
		}
		return fmt.Errorf("session %s may still be running (playbook %s, %s): cpb cannot tell, so it does not resume it. Two processes on one session id corrupt it; %s",
			id, s.dir.label, why, pick)
	}
	return fmt.Errorf("session %s is still running (playbook %s, pid %d, since %s). Two processes on one session id corrupt it; %s",
		id, s.dir.label, s.f.PID, formatAge(time.UnixMilli(s.f.StartedAt)), pick)
}

// The exit line: after claude exits under cpb run or a launcher, cpb names
// the command that resumes the session in this playbook. Claude Code's own
// "claude --resume" line would open it under ~/.claude instead.

// stderrTTY is whether stderr is a terminal; a variable for tests.
var stderrTTY = func() bool { return isTerminal(os.Stderr) }

// printMode reports whether claude ran non-interactively (-p / --print).
func printMode(claudeArgs []string) bool {
	for _, a := range claudeArgs {
		if a == "--" {
			return false
		}
		if a == "-p" || a == "--print" || strings.HasPrefix(a, "--print=") {
			return true
		}
	}
	return false
}

// transcriptStamps maps each transcript in configDir to its modification
// time: what a launch is compared with afterwards.
func transcriptStamps(configDir string) map[string]time.Time {
	m, _ := filepath.Glob(filepath.Join(configDir, "projects", "*", "*.jsonl"))
	out := make(map[string]time.Time, len(m))
	for _, p := range m {
		if fi, err := os.Stat(p); err == nil {
			out[p] = fi.ModTime()
		}
	}
	return out
}

// sessionSince is the session a launch left in configDir: the newest
// transcript that is new, or changed, since before (transcriptStamps,
// taken just before the launch), and that no other live process holds. ""
// when there is none (the session never got a message).
//
// The files are compared with themselves, never with the clock: a file's
// time comes from the kernel's coarser clock, which runs some milliseconds
// behind time.Now, so a transcript a launch wrote can read as written
// before the launch began (across a second boundary, now and then).
func sessionSince(configDir string, before map[string]time.Time) string {
	m, _ := filepath.Glob(filepath.Join(configDir, "projects", "*", "*.jsonl"))
	held := map[string]bool{}
	for _, s := range readSessionFiles([]sessionDir{{path: configDir}}) {
		held[s.f.SessionID] = true
	}
	var best string
	var bestT time.Time
	for _, p := range m {
		id := strings.TrimSuffix(filepath.Base(p), ".jsonl")
		if !grammar.ValidSessionID(id) || strings.HasPrefix(id, "agent-") || held[id] {
			continue
		}
		fi, err := os.Stat(p)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if t, seen := before[p]; seen && t.Equal(fi.ModTime()) {
			continue
		}
		if best == "" || fi.ModTime().After(bestT) {
			best, bestT = id, fi.ModTime()
		}
	}
	return best
}

// wantsResumeLine is whether a launch prints the exit line: stderr is a
// terminal and claude is interactive.
func wantsResumeLine(claudeArgs []string) bool { return stderrTTY() && !printMode(claudeArgs) }

// printResumeLine prints the exit line for a local launch of d, before
// being the transcripts it found when the launch began.
func printResumeLine(d sessionDir, before map[string]time.Time) {
	if id := sessionSince(d.path, before); id != "" {
		fmt.Fprintf(os.Stderr, "Resume this playbook's session with: %s\n", d.resumeCommand(id))
	}
}

// formatAge is a time as an age ("3 hours ago").
func formatAge(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	case d < 48*time.Hour:
		return "yesterday"
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}
