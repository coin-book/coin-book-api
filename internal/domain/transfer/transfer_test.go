package transfer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPick(t *testing.T) {
	assert.NotNil(t, pick([]edge{{from: "a", to: "b", rivals: 1}}), "single candidate without rivals")
	assert.Nil(t, pick([]edge{{from: "a", to: "b", rivals: 2}}), "the candidate could belong to someone else")
	assert.Nil(t, pick([]edge{{from: "a", to: "b"}, {from: "a", to: "c"}}), "two candidates are the user's call")
	got := pick([]edge{{from: "a", to: "b"}, {from: "a", to: "c", strong: true}})
	if assert.NotNil(t, got) {
		assert.Equal(t, "c", got.to, "the account the payment names wins")
	}
}

func TestOrientPutsTheNegativeLegOut(t *testing.T) {
	assert.Equal(t, Pair{OutTxID: "a", InTxID: "b"}, orient("a", -100, "b"))
	assert.Equal(t, Pair{OutTxID: "b", InTxID: "a"}, orient("a", 100, "b"))
}
