package testutil

import (
	"encoding/json"
	"flag"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

var update = flag.Bool("update", false, "rewrite golden files instead of comparing against them")

func AssertGoldenJSON(t *testing.T, path string, got any) {
	t.Helper()
	actual, err := json.MarshalIndent(got, "", "  ")
	require.NoError(t, err)
	actual = append(actual, '\n')
	if *update {
		require.NoError(t, os.WriteFile(path, actual, 0o600))
		return
	}
	want, err := os.ReadFile(path) //nolint:gosec // G304: test fixture path chosen by the test
	require.NoError(t, err, "golden file missing; run go test with -update")
	require.JSONEq(t, string(want), string(actual))
}
