package sensitive

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLogNeverContainsFullAccountNumber(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	logger.Info("import", "account", AccountNumber("19-2000145399/0800"), "message", MessageText("za obed"))

	assert.NotContains(t, buf.String(), "2000145399")
	assert.Contains(t, buf.String(), "****0800")
	assert.NotContains(t, buf.String(), "za obed")
}
