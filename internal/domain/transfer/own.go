package transfer

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rotisserie/eris"

	"coin-book-api/internal/domain/bank"
	"coin-book-api/internal/platform/db"
)

const (
	ByNumber       MatchedBy = "number"
	ByCounterparty MatchedBy = "counterparty"
)

var ErrNotOwnAccount = eris.New("transfer: the account is the transaction's own account or does not exist")

// ownTransferSignals are how banks label a payment between the client's own accounts:
// mBank operation types (parser/mbankcz) and its category (categorization/bankhints.go).
var ownTransferSignals = []string{"VLASTNÍ PŘEVOD", "PŘEVOD NA MSPOŘENÍ", "PŘEVOD Z MSPOŘENÍ", "Převod mezi vlastními účty"}

type ownAccounts struct {
	byNumber       map[string]uuid.UUID
	byCounterparty map[string]uuid.UUID
}

func loadOwnAccounts(ctx context.Context, q db.Querier) (*ownAccounts, error) {
	own := &ownAccounts{byNumber: map[string]uuid.UUID{}, byCounterparty: map[string]uuid.UUID{}}
	rows, err := q.Query(ctx, `SELECT account_number, id FROM account WHERE account_number IS NOT NULL`)
	if err != nil {
		return nil, eris.Wrap(err, "list own account numbers")
	}
	for rows.Next() {
		var number string
		var id uuid.UUID
		if err := rows.Scan(&number, &id); err != nil {
			rows.Close()
			return nil, eris.Wrap(err, "read own account number")
		}
		own.byNumber[bank.CanonicalNumber(number)] = id
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, eris.Wrap(err, "read own account numbers")
	}
	rows, err = q.Query(ctx, `SELECT counterparty, account_id FROM own_transfer_rule`)
	if err != nil {
		return nil, eris.Wrap(err, "list own transfer rules")
	}
	defer rows.Close()
	for rows.Next() {
		var counterparty string
		var id uuid.UUID
		if err := rows.Scan(&counterparty, &id); err != nil {
			return nil, eris.Wrap(err, "read own transfer rule")
		}
		own.byCounterparty[counterparty] = id
	}
	return own, eris.Wrap(rows.Err(), "read own transfer rules")
}

type ownMatch struct {
	transactionID string
	accountID     uuid.UUID
	by            MatchedBy
}

// DetectOwn marks transactions in $1 (all when nil) that pay to or from an own account,
// known by its number or by a counterparty the user named. Run it after AutoMatch, so a
// payment whose other leg is imported becomes a pair instead.
func (s *Service) DetectOwn(ctx context.Context, q db.Querier, transactionIDs []string) (int, error) {
	own, err := loadOwnAccounts(ctx, q)
	if err != nil {
		return 0, err
	}
	if len(own.byNumber) == 0 && len(own.byCounterparty) == 0 {
		return 0, nil
	}
	var filter any
	if transactionIDs != nil {
		filter = transactionIDs
	}
	rows, err := q.Query(ctx, `
		SELECT t.id, t.account_id, coalesce(t.counterparty_account, ''), lower(coalesce(t.counterparty, ''))
		FROM transaction t
		WHERE ($1::text[] IS NULL OR t.id = ANY($1)) AND t.amount_minor <> 0
		  AND (t.counterparty_account IS NOT NULL OR t.counterparty IS NOT NULL)
		  AND NOT EXISTS (SELECT 1 FROM transfer_leg tl WHERE tl.transaction_id = t.id)
		  AND NOT EXISTS (SELECT 1 FROM not_transfer nt WHERE nt.transaction_id = t.id)`, filter)
	if err != nil {
		return 0, eris.Wrap(err, "find own transfer candidates")
	}
	var matches []ownMatch
	for rows.Next() {
		var id, number, counterparty string
		var accountID uuid.UUID
		if err := rows.Scan(&id, &accountID, &number, &counterparty); err != nil {
			rows.Close()
			return 0, eris.Wrap(err, "read own transfer candidate")
		}
		if target, ok := own.byNumber[bank.CanonicalNumber(number)]; ok && number != "" && target != accountID {
			matches = append(matches, ownMatch{id, target, ByNumber})
		} else if target, ok := own.byCounterparty[counterparty]; ok && target != accountID {
			matches = append(matches, ownMatch{id, target, ByCounterparty})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, eris.Wrap(err, "read own transfer candidates")
	}
	return len(matches), s.insertOwn(ctx, q, matches)
}

func (s *Service) insertOwn(ctx context.Context, q db.Querier, matches []ownMatch) error {
	if len(matches) == 0 {
		return nil
	}
	ids := make([]string, len(matches))
	accounts := make([]uuid.UUID, len(matches))
	by := make([]string, len(matches))
	for i, m := range matches {
		ids[i], accounts[i], by[i] = m.transactionID, m.accountID, string(m.by)
	}
	_, err := q.Exec(ctx, `
		INSERT INTO own_transfer (transaction_id, account_id, matched_by)
		SELECT * FROM unnest($1::text[], $2::uuid[], $3::text[])
		ON CONFLICT (transaction_id) DO UPDATE SET account_id = EXCLUDED.account_id, matched_by = EXCLUDED.matched_by`,
		ids, accounts, by)
	if err != nil {
		return eris.Wrap(err, "insert own transfers")
	}
	return s.categorizer.ClearAutomatic(ctx, q, ids)
}

// MarkOwn records by hand that a transaction pays to or from accountID. With remember,
// every other payment to the same counterparty follows; it returns how many did.
func (s *Service) MarkOwn(ctx context.Context, transactionID string, accountID uuid.UUID, remember bool) (int, error) {
	var others int
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		var counterparty string
		var paired bool
		err := tx.QueryRow(ctx, `
			SELECT lower(coalesce(t.counterparty, '')),
				EXISTS (SELECT 1 FROM transfer_pair tp WHERE tp.out_tx_id = t.id OR tp.in_tx_id = t.id)
			FROM transaction t JOIN account a ON a.id = $2 AND a.id <> t.account_id
			WHERE t.id = $1`, transactionID, accountID).Scan(&counterparty, &paired)
		if err != nil || paired {
			return ErrNotOwnAccount
		}
		if _, err := tx.Exec(ctx, `DELETE FROM not_transfer WHERE transaction_id = $1`, transactionID); err != nil {
			return eris.Wrap(err, "forget not-a-transfer")
		}
		if err := s.insertOwn(ctx, tx, []ownMatch{{transactionID, accountID, Manual}}); err != nil {
			return err
		}
		if !remember || counterparty == "" {
			return nil
		}
		if _, err := tx.Exec(ctx, `INSERT INTO own_transfer_rule (counterparty, account_id) VALUES ($1, $2)
			ON CONFLICT (counterparty) DO UPDATE SET account_id = EXCLUDED.account_id`, counterparty, accountID); err != nil {
			return eris.Wrap(err, "save own transfer rule")
		}
		others, err = s.DetectOwn(ctx, tx, nil)
		return err
	})
	return others, err
}

// Unpair undoes a transfer. A one-leg transfer is also remembered as not a transfer, so
// detection by account number does not mark it again.
func (s *Service) Unpair(ctx context.Context, transactionID string) error {
	return db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM transfer_pair WHERE out_tx_id = $1 OR in_tx_id = $1`, transactionID)
		if err != nil {
			return eris.Wrap(err, "delete transfer pair")
		}
		if tag.RowsAffected() > 0 {
			return nil
		}
		tag, err = tx.Exec(ctx, `DELETE FROM own_transfer WHERE transaction_id = $1`, transactionID)
		if err != nil {
			return eris.Wrap(err, "delete own transfer")
		}
		if tag.RowsAffected() == 0 {
			return ErrNotPaired
		}
		_, err = tx.Exec(ctx, `INSERT INTO not_transfer (transaction_id) VALUES ($1) ON CONFLICT DO NOTHING`, transactionID)
		return eris.Wrap(err, "remember not a transfer")
	})
}

type OwnAccountCandidate struct {
	AccountNumber string
	BankID        string
	Counterparty  string
	Transactions  int
	OutMinor      int64
	InMinor       int64
	Currency      string
	LastBookedOn  time.Time
}

// OwnAccountCandidates lists counterparty accounts the bank itself calls the client's own,
// that coin-book does not know yet: the user's likely savings, pockets and brokers.
func (s *Service) OwnAccountCandidates(ctx context.Context) ([]OwnAccountCandidate, error) {
	own, err := loadOwnAccounts(ctx, s.pool)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT t.counterparty_account, coalesce(t.counterparty, ''), t.amount_minor, t.currency, t.booked_on
		FROM transaction t
		WHERE t.counterparty_account IS NOT NULL
		  AND (t.operation_type = ANY($1) OR t.bank_category = ANY($1))
		  AND NOT EXISTS (SELECT 1 FROM transfer_leg tl WHERE tl.transaction_id = t.id)
		ORDER BY t.booked_on`, ownTransferSignals)
	if err != nil {
		return nil, eris.Wrap(err, "list own account candidates")
	}
	defer rows.Close()
	byNumber := map[string]*OwnAccountCandidate{}
	names := map[string]map[string]int{}
	for rows.Next() {
		var number, counterparty, currency string
		var amount int64
		var booked time.Time
		if err := rows.Scan(&number, &counterparty, &amount, &currency, &booked); err != nil {
			return nil, eris.Wrap(err, "read own account candidate")
		}
		number = bank.CanonicalNumber(number)
		if _, known := own.byNumber[number]; known {
			continue
		}
		c := byNumber[number]
		if c == nil {
			c = &OwnAccountCandidate{AccountNumber: number, BankID: bank.CandidateID(number), Currency: currency}
			byNumber[number], names[number] = c, map[string]int{}
		}
		c.Transactions++
		c.LastBookedOn = booked
		if amount < 0 {
			c.OutMinor -= amount
		} else {
			c.InMinor += amount
		}
		if counterparty != "" {
			names[number][counterparty]++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, eris.Wrap(err, "read own account candidates")
	}
	out := make([]OwnAccountCandidate, 0, len(byNumber))
	for number, c := range byNumber {
		c.Counterparty = mostFrequent(names[number])
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Transactions > out[j].Transactions })
	return out, nil
}

func mostFrequent(counts map[string]int) string {
	best, bestCount := "", 0
	for name, n := range counts {
		if n > bestCount || (n == bestCount && strings.Compare(name, best) < 0) {
			best, bestCount = name, n
		}
	}
	return best
}
