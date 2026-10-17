package categorization

import (
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"coin-book-api/internal/domain/importing/parser/mbankcz"
)

const holdoutEvery = 5

// Measures the classifier on a real mBank export kept outside the repository, labelling
// rows by the bank's own categories; prints aggregate numbers only.
func TestClassifierOnRealExport(t *testing.T) {
	path := os.Getenv("COINBOOK_REAL_MBANK_CZ")
	if path == "" {
		t.Skip("set COINBOOK_REAL_MBANK_CZ to a real mBank CZ export")
	}
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	st, err := mbankcz.Parser{}.Parse(f)
	require.NoError(t, err)

	var labelled []Example
	for _, r := range st.Accounts[0].Rows {
		code := bankCategoryHints[r.BankCategory]
		if code == "" || code == "transfer.own" {
			continue
		}
		labelled = append(labelled, Example{
			Candidate: Candidate{AmountMinor: r.AmountMinor, Description: r.Description, Counterparty: r.Counterparty,
				OperationType: r.OperationType, BankCategory: r.BankCategory},
			CategoryID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(code)), Source: SourceManual,
		})
	}
	for _, withBank := range []bool{true, false} {
		var train, test []Example
		for i, e := range labelled {
			if !withBank {
				e.Candidate.BankCategory = ""
			}
			if i%holdoutEvery == 0 {
				test = append(test, e)
			} else {
				train = append(train, e)
			}
		}
		model := Train(train)
		var top1, top3, confident, confidentRight, seenCounterparty, seenRight int
		known := map[string]map[uuid.UUID]int{}
		for _, e := range train {
			if known[e.Candidate.Counterparty] == nil {
				known[e.Candidate.Counterparty] = map[uuid.UUID]int{}
			}
			known[e.Candidate.Counterparty][e.CategoryID]++
		}
		for _, e := range test {
			p := model.Predict(e.Candidate, 3)
			if len(p) > 0 && p[0].CategoryID == e.CategoryID {
				top1++
			}
			for _, q := range p {
				if q.CategoryID == e.CategoryID {
					top3++
				}
			}
			if len(p) > 0 && p[0].Confidence >= 0.9 {
				confident++
				if p[0].CategoryID == e.CategoryID {
					confidentRight++
				}
			}
			if counts := known[e.Candidate.Counterparty]; counts != nil && e.Candidate.Counterparty != "" {
				seenCounterparty++
				best, bestCount := uuid.Nil, 0
				for id, n := range counts {
					if n > bestCount {
						best, bestCount = id, n
					}
				}
				if best == e.CategoryID {
					seenRight++
				}
			}
		}
		n := float64(len(test))
		t.Logf("with_bank_category=%v train=%d test=%d top1=%.1f%% top3=%.1f%% confident>=0.9: coverage=%.1f%% precision=%.1f%% | old exact-counterparty: coverage=%.1f%% precision=%.1f%%",
			withBank, len(train), len(test), 100*float64(top1)/n, 100*float64(top3)/n,
			100*float64(confident)/n, 100*float64(confidentRight)/max(float64(confident), 1),
			100*float64(seenCounterparty)/n, 100*float64(seenRight)/max(float64(seenCounterparty), 1))
	}
}
