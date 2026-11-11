package grammar

import (
	"strings"
	"testing"
)

// The cpb:<name> rule holds at every grammar site that takes a reference:
// a playbook's or a set's SET VAR FROM, an account's credential and its
// header, and an MCP server's env and header. A good name parses at each;
// a bad one is refused, and the error never quotes the name.
func TestCPBRefRuleAtEveryGrammarSite(t *testing.T) {
	sites := []string{
		"ALTER PLAYBOOK p SET VAR K FROM '%s';",
		"CREATE ENV e SET K FROM '%s';",
		"CREATE ACCOUNT a SET provider = 'anthropic', claude_token FROM '%s';",
		"CREATE ACCOUNT g SET provider = 'gateway', base_url = 'https://router.example.com/v1', auth = 'none', header.X-Id FROM '%s';",
		"ALTER PLAYBOOK p ADD MCP SERVER fs COMMAND 'npx' VAR API_TOKEN FROM '%s';",
		"ALTER PLAYBOOK p ADD MCP SERVER web URL 'https://mcp.example.com/mcp' HEADER 'Authorization' FROM '%s';",
	}
	for _, site := range sites {
		if _, err := ParseFile(strings.Replace(site, "%s", "cpb:good-name", 1)); err != nil {
			t.Errorf("a good name refused at %q: %v", site, err)
		}
		_, err := ParseFile(strings.Replace(site, "%s", "cpb:Bad.Name", 1))
		if err == nil {
			t.Errorf("a bad name passed at %q", site)
			continue
		}
		if strings.Contains(err.Error(), "Bad.Name") {
			t.Errorf("the error at %q quotes the name: %v", site, err)
		}
	}
}
