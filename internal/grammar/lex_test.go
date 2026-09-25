package grammar

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func texts(stmts [][]Token) [][]string {
	var out [][]string
	for _, st := range stmts {
		var ws []string
		for _, t := range st {
			ws = append(ws, t.Text)
		}
		out = append(out, ws)
	}
	return out
}

func TestLexFile(t *testing.T) {
	cases := []struct {
		src  string
		want [][]string
	}{
		{"SHOW ENVS", [][]string{{"SHOW", "ENVS"}}},
		{"A;B", [][]string{{"A"}, {"B"}}},
		{";; A ;;", [][]string{{"A"}}},
		{"A -- comment ; not a split\nB;", [][]string{{"A", "B"}}},
		{"-- only a comment\n", nil},
		{"a--b", [][]string{{"a--b"}}},
		{"A --dry-run --\nB --\tc", [][]string{{"A", "--dry-run", "B"}}},
		{"A --", [][]string{{"A"}}},
		{"SET A='hello world'", [][]string{{"SET", "A=hello world"}}},
		{`SET A="x y" B='it''s'`, [][]string{{"SET", "A=x y", "B=it's"}}},
		{"DESCRIBE 'semi;colon -- not a comment'", [][]string{{"DESCRIBE", "semi;colon -- not a comment"}}},
		{"X=''", [][]string{{"X="}}},
		{"''", [][]string{{""}}},
		{"A\tB\r\nC", [][]string{{"A", "B", "C"}}},
	}
	for _, tc := range cases {
		got, err := lexFile(tc.src)
		if err != nil {
			t.Errorf("lexFile(%q): %v", tc.src, err)
			continue
		}
		if !reflect.DeepEqual(texts(got), tc.want) {
			t.Errorf("lexFile(%q)\n got %q\nwant %q", tc.src, texts(got), tc.want)
		}
	}
}

func TestLexQuotedFlag(t *testing.T) {
	got, err := lexFile("SET 'SET' A='1'")
	if err != nil {
		t.Fatal(err)
	}
	quoted := []bool{got[0][0].Quoted, got[0][1].Quoted, got[0][2].Quoted}
	if !reflect.DeepEqual(quoted, []bool{false, true, true}) {
		t.Errorf("Quoted flags %v", quoted)
	}
}

func TestLexPositions(t *testing.T) {
	got, err := lexFile("\n  ALTER 'multi\nline' X\n;Y")
	if err != nil {
		t.Fatal(err)
	}
	want := []Pos{{Line: 2, Col: 3}, {Line: 2, Col: 9}, {Line: 3, Col: 7}}
	for i, p := range want {
		if got[0][i].Pos != p {
			t.Errorf("token %d at %v, want %v", i, got[0][i].Pos, p)
		}
	}
	if got[1][0].Pos != (Pos{Line: 4, Col: 2}) {
		t.Errorf("Y at %v, want line 4, col 2", got[1][0].Pos)
	}
}

func TestLexUnterminatedQuote(t *testing.T) {
	_, err := lexFile("ALTER ENV e DESCRIBE 'never closed")
	var pe *Error
	if !errors.As(err, &pe) || !strings.Contains(pe.Msg, "unterminated quote") || pe.Pos != (Pos{Line: 1, Col: 22}) {
		t.Errorf("got %v", err)
	}
}
