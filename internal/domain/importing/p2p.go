package importing

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rotisserie/eris"

	"coin-book-api/internal/domain/importing/statement"
	"coin-book-api/internal/platform/db"
)

// plannedP2P is one platform account's snapshot; known marks loans an earlier export of the
// same day already recorded, which keep their first values.
type plannedP2P struct {
	account  int
	snapshot *statement.P2PSnapshot
	known    []bool
}

func markKnownPositions(ctx context.Context, q db.Querier, p *plan) error {
	for _, planned := range p.p2p {
		planned.known = make([]bool, len(planned.snapshot.Positions))
		existing := p.accounts[planned.account].existing
		if existing == nil {
			continue
		}
		rows, err := q.Query(ctx, `SELECT loan_number FROM p2p_position WHERE account_id = $1 AND snapshot_on = $2`,
			existing.ID, planned.snapshot.TakenOn)
		if err != nil {
			return eris.Wrap(err, "find recorded loans")
		}
		numbers, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return eris.Wrap(err, "read recorded loans")
		}
		recorded := make(map[string]bool, len(numbers))
		for _, n := range numbers {
			recorded[n] = true
		}
		for i, pos := range planned.snapshot.Positions {
			planned.known[i] = recorded[pos.LoanNumber]
		}
	}
	return nil
}

func writePositions(ctx context.Context, tx pgx.Tx, planned []*plannedP2P, accountIDs []uuid.UUID, currencies []string, batchID uuid.UUID) error {
	var rows [][]any
	for _, p := range planned {
		for i, pos := range p.snapshot.Positions {
			if p.known[i] {
				continue
			}
			rows = append(rows, []any{accountIDs[p.account], p.snapshot.TakenOn, pos.LoanNumber, pos.Name, pos.Repayment,
				pos.Status, pos.RepaymentStatus, currencies[p.account], pos.InvestedMinor, pos.OutstandingMinor,
				numeric(pos.InterestRate), pos.InvestedOn, pos.NextPaymentOn, pos.NextPaymentMinor, pos.FinalPaymentOn,
				pos.DelayDays, pos.OverdueMinor, pos.InterestPaidMinor, pos.PenaltyInterestMinor, pos.PrincipalReturnedMinor,
				pos.MarketGainMinor, pos.FeesMinor, batchID, pos.Line})
		}
	}
	err := copyThroughStaging(ctx, tx, "p2p_position", []string{"account_id", "snapshot_on", "loan_number", "name",
		"repayment", "status", "repayment_status", "currency", "invested_minor", "outstanding_minor", "interest_rate",
		"invested_on", "next_payment_on", "next_payment_minor", "final_payment_on", "delay_days", "overdue_minor",
		"interest_paid_minor", "penalty_interest_minor", "principal_returned_minor", "market_gain_minor", "fees_minor",
		"import_batch_id", "source_line"}, rows)
	return eris.Wrap(err, "insert P2P positions")
}
