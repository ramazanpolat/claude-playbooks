package manifest

import (
	"fmt"
	"strings"
)

// SandboxKeys are the [sandbox] table's keys, in the order SHOW CREATE and
// SHOW PLAYBOOK write them. ALTER PLAYBOOK … SET SANDBOX <key>=<value> and
// UNSET SANDBOX <key> name them as they are.
var SandboxKeys = []string{"always", "backend", "host", "workdir", "mounts", "allow_net", "secrets", "claude_version", "share_skills"}

// IsSandboxKey reports whether key is one of SandboxKeys.
func IsSandboxKey(key string) bool {
	for _, k := range SandboxKeys {
		if k == key {
			return true
		}
	}
	return false
}

// SandboxFlag reports whether key holds true or false (always,
// share_skills); the others hold a value or, for mounts and allow_net, a
// comma-separated list.
func SandboxFlag(key string) bool { return key == "always" || key == "share_skills" }

// SetKey sets one key from its statement form and validates the block.
func (s *Sandbox) SetKey(key, value string) error {
	switch key {
	case "always", "share_skills":
		var b bool
		switch value {
		case "true":
			b = true
		case "false":
		default:
			return fmt.Errorf("sandbox.%s takes true or false, not %q", key, value)
		}
		if key == "always" {
			s.Always = b
		} else {
			s.ShareSkills = b
		}
	case "backend":
		s.Backend = value
	case "host":
		s.Host = value
	case "workdir":
		s.Workdir = value
	case "secrets":
		s.Secrets = value
	case "claude_version":
		s.ClaudeVersion = value
	case "mounts":
		s.Mounts = splitSandboxList(value)
	case "allow_net":
		s.AllowNet = splitSandboxList(value)
	default:
		return fmt.Errorf("%s is not a [sandbox] key (%s)", key, strings.Join(SandboxKeys, ", "))
	}
	return s.validate()
}

// UnsetKey clears one key.
func (s *Sandbox) UnsetKey(key string) error {
	switch key {
	case "always":
		s.Always = false
	case "share_skills":
		s.ShareSkills = false
	case "backend":
		s.Backend = ""
	case "host":
		s.Host = ""
	case "workdir":
		s.Workdir = ""
	case "secrets":
		s.Secrets = ""
	case "claude_version":
		s.ClaudeVersion = ""
	case "mounts":
		s.Mounts = nil
	case "allow_net":
		s.AllowNet = nil
	default:
		return fmt.Errorf("%s is not a [sandbox] key (%s)", key, strings.Join(SandboxKeys, ", "))
	}
	return nil
}

// Settings is every set key but always, in SandboxKeys order, in its
// statement form: what SET SANDBOX <key>=<value> … writes back.
func (s *Sandbox) Settings() [][2]string {
	if s == nil {
		return nil
	}
	var out [][2]string
	add := func(k, v string) {
		if v != "" {
			out = append(out, [2]string{k, v})
		}
	}
	add("backend", s.Backend)
	add("host", s.Host)
	add("workdir", s.Workdir)
	add("mounts", strings.Join(s.Mounts, ","))
	add("allow_net", strings.Join(s.AllowNet, ","))
	add("secrets", s.Secrets)
	add("claude_version", s.ClaudeVersion)
	if s.ShareSkills {
		add("share_skills", "true")
	}
	return out
}

// splitSandboxList is a comma-separated list, blanks dropped.
func splitSandboxList(v string) []string {
	var out []string
	for _, item := range strings.Split(v, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}
