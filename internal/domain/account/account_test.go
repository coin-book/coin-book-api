package account

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOnlyAccountsValuedAlikeMerge(t *testing.T) {
	assert.True(t, mergeable(Broker, Current), "a broker created by hand into the one an import created")
	assert.True(t, mergeable(Mortgage, Loan))
	assert.True(t, mergeable(P2P, Pension))
	assert.False(t, mergeable(Mortgage, Current))
	assert.False(t, mergeable(Property, Savings))
}
