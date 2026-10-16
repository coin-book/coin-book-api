//go:build integration

package acceptance

import (
	"testing"

	"coin-book-api/internal/testutil"
)

func TestMain(m *testing.M) { testutil.RunWithPostgres(m, &pg) }
