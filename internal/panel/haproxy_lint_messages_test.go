package panel

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// Every lint message format must have a stable key, so the UI can translate it.
func TestLintMessageKeysCoverAllFormats(t *testing.T) {
	fset := token.NewFileSet()
	seen := 0
	for _, file := range []string{"haproxy_lint.go", "haproxy_lint_checks.go"} {
		f, err := parser.ParseFile(fset, file, nil, 0)
		require.NoError(t, err)
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			index := -1
			switch fn := call.Fun.(type) {
			case *ast.SelectorExpr:
				if fn.Sel.Name == "errorf" || fn.Sel.Name == "warnf" {
					index = 3
				}
			case *ast.Ident:
				if fn.Name == "newLintIssue" {
					index = 5
				}
			}
			if index < 0 || len(call.Args) <= index {
				return true
			}
			lit, ok := call.Args[index].(*ast.BasicLit)
			if !ok {
				return true
			}
			format, err := strconv.Unquote(lit.Value)
			require.NoError(t, err)
			_, ok = lintMessageKeys[format]
			require.True(t, ok, "no lint message key for %q (%s)", format, fset.Position(lit.Pos()))
			seen++
			return true
		})
	}
	require.Greater(t, seen, 80)

	result := LintHAProxyConfig("frontend fe\n    bind :99999\n")
	require.NotEmpty(t, result.Issues)
	for _, issue := range result.Issues {
		require.NotEmpty(t, issue.MessageKey, issue.Message)
	}
}
