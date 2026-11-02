package transfer

import (
	"cmp"
	"context"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/rotisserie/eris"

	"coin-book-api/internal/platform/db"
)

// CounterpartWindowDays is wider than MatchWindowDays: the user judges each candidate, so a
// transfer the bank booked late can still be found.
const CounterpartWindowDays = 7

const maxCounterparts = 20

// counterpartPool is read before the number matches move to the front, so one on a farther day is not cut off.
const counterpartPool = 100

type Counterpart struct {
	TransactionID string
	// NumberMatches: one leg names the other's account number as its counterparty.
	NumberMatches bool
}

// Counterparts lists the movements that could be the other leg of a transfer for transactionID,
// including those already in another transfer, likeliest first: one naming the other's account,
// then the same currency, then the nearest day. One in another currency must agree at about the
// ČNB rate. Its current partner is left out.
func (s *Service) Counterparts(ctx context.Context, transactionID string) ([]Counterpart, error) {
	own, err := loadOwnAccounts(ctx, s.pool)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		WITH n AS (
			SELECT t.id, t.account_id, t.amount_minor, t.currency, t.booked_on, t.counterparty_account
			FROM transaction t WHERE t.id = $1 AND t.amount_minor <> 0
		), arms AS (
			SELECT c.id FROM n JOIN transaction c
				ON c.currency = n.currency AND c.amount_minor = -n.amount_minor
				AND c.booked_on BETWEEN n.booked_on - $2::int AND n.booked_on + $2::int AND c.account_id <> n.account_id
			UNION ALL
			SELECT c.id FROM n JOIN transaction c
				ON c.currency <> n.currency AND sign(c.amount_minor) = -sign(n.amount_minor)
				AND c.booked_on BETWEEN n.booked_on - $2::int AND n.booked_on + $2::int AND c.account_id <> n.account_id
		)
		SELECT c.id, n.account_id, coalesce(n.counterparty_account, ''), c.account_id, coalesce(c.counterparty_account, ''),
			n.amount_minor, n.currency, c.amount_minor, c.currency,
			CASE WHEN c.currency <> n.currency THEN `+czkPerUnitSQL("n.currency", "n.booked_on")+` END,
			CASE WHEN c.currency <> n.currency THEN `+czkPerUnitSQL("c.currency", "c.booked_on")+` END
		FROM arms JOIN transaction c ON c.id = arms.id
		CROSS JOIN n
		WHERE NOT EXISTS (SELECT 1 FROM loan_payment lp WHERE lp.transaction_id = c.id)
		  AND NOT EXISTS (SELECT 1 FROM transfer_pair tp
			WHERE (tp.out_tx_id = n.id AND tp.in_tx_id = c.id) OR (tp.out_tx_id = c.id AND tp.in_tx_id = n.id))
		ORDER BY c.currency = n.currency DESC, abs(c.booked_on - n.booked_on), c.booked_on, c.id
		LIMIT $3`, transactionID, CounterpartWindowDays, counterpartPool)
	if err != nil {
		return nil, eris.Wrap(err, "find transfer counterparts")
	}
	type candidate struct {
		Counterpart
		plausible bool
	}
	candidates, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (candidate, error) {
		var c candidate
		var self, other leg
		var selfMove, otherMove movement
		err := row.Scan(&c.TransactionID, &self.accountID, &self.counterpartyAccount, &other.accountID, &other.counterpartyAccount,
			&selfMove.minor, &selfMove.currency, &otherMove.minor, &otherMove.currency, &selfMove.czkPerUnit, &otherMove.czkPerUnit)
		c.NumberMatches = own.namesAccountOf(self, other) || own.namesAccountOf(other, self)
		c.plausible = selfMove.currency == otherMove.currency || plausibleConversion(selfMove, otherMove)
		return c, err
	})
	if err != nil {
		return nil, eris.Wrap(err, "read transfer counterparts")
	}
	counterparts := make([]Counterpart, 0, len(candidates))
	for _, c := range candidates {
		if c.plausible {
			counterparts = append(counterparts, c.Counterpart)
		}
	}
	slices.SortStableFunc(counterparts, func(a, b Counterpart) int {
		return cmp.Compare(boolRank(b.NumberMatches), boolRank(a.NumberMatches))
	})
	return counterparts[:min(len(counterparts), maxCounterparts)], nil
}

func boolRank(b bool) int {
	if b {
		return 1
	}
	return 0
}

// PairWithCounterpart makes counterpartID the other leg of transactionID's transfer. Any transfer either of them
// was part of is undone; it returns the former partners that are left with no transfer.
func (s *Service) PairWithCounterpart(ctx context.Context, transactionID, counterpartID string) ([]string, error) {
	var released []string
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		legs := []string{transactionID, counterpartID}
		// Locked in id order: two concurrent pairings by hand sharing a leg would otherwise both pass the check
		// and the second insert would break the pair's unique legs.
		if _, err := tx.Exec(ctx, `SELECT 1 FROM transaction WHERE id = ANY($1) ORDER BY id FOR NO KEY UPDATE`, legs); err != nil {
			return eris.Wrap(err, "lock the transfer legs")
		}
		var amount int64
		var ok bool
		err := tx.QueryRow(ctx, `
			SELECT n.amount_minor, n.amount_minor <> 0 AND sign(n.amount_minor) = -sign(c.amount_minor)
				AND n.account_id <> c.account_id
				AND NOT EXISTS (SELECT 1 FROM loan_payment lp WHERE lp.transaction_id IN (n.id, c.id))
			FROM transaction n, transaction c WHERE n.id = $1 AND c.id = $2`, transactionID, counterpartID).Scan(&amount, &ok)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && !ok {
			return ErrNotPairable
		}
		if err != nil {
			return eris.Wrap(err, "check the transfer legs")
		}
		rows, err := tx.Query(ctx, `DELETE FROM transfer_pair WHERE out_tx_id = ANY($1) OR in_tx_id = ANY($1)
			RETURNING out_tx_id, in_tx_id`, legs)
		if err != nil {
			return eris.Wrap(err, "undo the previous transfers")
		}
		undone, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Pair])
		if err != nil {
			return eris.Wrap(err, "read the previous transfers")
		}
		for _, p := range undone {
			for _, id := range []string{p.OutTxID, p.InTxID} {
				if !slices.Contains(legs, id) {
					released = append(released, id)
				}
			}
		}
		return s.insert(ctx, tx, []Pair{orient(transactionID, amount, counterpartID)}, Manual)
	})
	return released, err
}
