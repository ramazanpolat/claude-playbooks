package grammar

import (
	"fmt"
	"strings"
)

// Token is one word of a statement.
type Token struct {
	Text string
	// Quoted reports that some part of the word was quoted in a playbook file.
	// A quoted word is never a keyword, which is how a file spells a key or
	// a name that collides with one. Words from argv are never Quoted: the
	// shell has already removed the quotes, so there is nothing to record.
	Quoted bool
	Pos    Pos
}

// Pos locates a token: a line and column in a playbook file, or a word number
// on the command line (1 = the word after "cpb").
type Pos struct {
	Line int
	Col  int
	Word int
}

func (p Pos) String() string {
	if p.Line > 0 {
		return fmt.Sprintf("line %d, col %d", p.Line, p.Col)
	}
	return fmt.Sprintf("word %d", p.Word)
}

// argTokens turns command-line arguments into tokens.
func argTokens(args []string) []Token {
	toks := make([]Token, len(args))
	for i, a := range args {
		toks[i] = Token{Text: a, Pos: Pos{Word: i + 1}}
	}
	return toks
}

// lexFile splits a playbook file into statements of tokens.
//
// The rules are SQL's where SQL has one: whitespace (newlines included)
// separates words, ';' ends a statement, and '--' followed by whitespace
// (or the end of the line) comments out the rest of the line. The trailing
// space is MySQL's rule, and it is what keeps a word like --dry-run a word.
// A word may contain quoted parts, shell-style ('A=hello world' and
// A='hello world' are the same word); single and double quotes both work
// and a doubled quote inside is a literal one. Empty statements (';;') are
// skipped.
func lexFile(src string) ([][]Token, error) {
	var (
		stmts     [][]Token
		cur       []Token
		line, col = 1, 0
		rs        = []rune(src)
	)
	for i := 0; i < len(rs); {
		r := rs[i]
		switch {
		case r == '\n':
			line, col = line+1, 0
			i++
			continue
		case r == ' ' || r == '\t' || r == '\r':
			col++
			i++
			continue
		case r == ';':
			col++
			i++
			if len(cur) > 0 {
				stmts = append(stmts, cur)
				cur = nil
			}
			continue
		case r == '-' && i+1 < len(rs) && rs[i+1] == '-' && (i+2 == len(rs) || strings.ContainsRune(" \t\r\n", rs[i+2])):
			for i < len(rs) && rs[i] != '\n' {
				i++
			}
			continue
		}

		// A word: runs until unquoted whitespace, ';' or the end.
		start := Pos{Line: line, Col: col + 1}
		var b strings.Builder
		quoted := false
		for i < len(rs) {
			r = rs[i]
			if r == ' ' || r == '\t' || r == '\r' || r == '\n' || r == ';' {
				break
			}
			if r != '\'' && r != '"' {
				b.WriteRune(r)
				col++
				i++
				continue
			}
			q, qpos := r, Pos{Line: line, Col: col + 1}
			quoted = true
			col++
			i++
			closed := false
			for i < len(rs) {
				c := rs[i]
				if c == q {
					if i+1 < len(rs) && rs[i+1] == q {
						b.WriteRune(q)
						col += 2
						i += 2
						continue
					}
					col++
					i++
					closed = true
					break
				}
				if c == '\n' {
					line, col = line+1, 0
				} else {
					col++
				}
				b.WriteRune(c)
				i++
			}
			if !closed {
				return nil, &Error{Pos: qpos, Msg: "unterminated quote"}
			}
		}
		cur = append(cur, Token{Text: b.String(), Quoted: quoted, Pos: start})
	}
	if len(cur) > 0 {
		stmts = append(stmts, cur)
	}
	return stmts, nil
}
