// Package bank is the register of banks (migration 004) and tells which bank an
// account number belongs to.
package bank

import (
	"context"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rotisserie/eris"

	"coin-book-api/internal/platform/db"
)

const (
	Revolut = "revolut"
	IBKR    = "ibkr"
)

type Bank struct {
	ID        string
	Country   *string
	Code      *string
	Name      string
	ShortName string
	BIC       *string
}

var (
	czechNumber = regexp.MustCompile(`/(\d{4})$`)
	iban        = regexp.MustCompile(`^(CZ|SK)\d{2}(\d{4})\d{16}$`)
)

// CandidateID derives the register id from an account number: the bank code after the
// slash of a Czech number, or the bank code inside a CZ/SK IBAN. Empty when it has none.
func CandidateID(number string) string {
	compact := strings.ToUpper(strings.ReplaceAll(number, " ", ""))
	if m := iban.FindStringSubmatch(compact); m != nil {
		return strings.ToLower(m[1]) + "-" + m[2]
	}
	if m := czechNumber.FindStringSubmatch(compact); m != nil {
		return "cz-" + m[1]
	}
	return ""
}

var (
	domesticNumber = regexp.MustCompile(`^(?:(\d{1,6})-)?(\d{2,10})/(\d{4})$`)
	domesticIBAN   = regexp.MustCompile(`^(?:CZ|SK)\d{2}(\d{4})(\d{6})(\d{10})$`)
)

// CanonicalNumber writes a CZ/SK account number one way, as mBank prints a counterparty
// ("19-123457/0800"), whether it came typed by hand, zero-padded or as an IBAN.
// Other numbers come back compacted and upper-cased.
func CanonicalNumber(number string) string {
	compact := strings.ToUpper(strings.ReplaceAll(number, " ", ""))
	var prefix, base, code string
	if m := domesticIBAN.FindStringSubmatch(compact); m != nil {
		code, prefix, base = m[1], m[2], m[3]
	} else if m := domesticNumber.FindStringSubmatch(compact); m != nil {
		prefix, base, code = m[1], m[2], m[3]
	} else {
		return compact
	}
	prefix, base = strings.TrimLeft(prefix, "0"), strings.TrimLeft(base, "0")
	if prefix == "" {
		return base + "/" + code
	}
	return prefix + "-" + base + "/" + code
}

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

func (r *Repo) List(ctx context.Context) ([]Bank, error) {
	rows, err := r.pool.Query(ctx, `SELECT id, country, code, name, short_name, bic FROM bank
		ORDER BY country NULLS LAST, short_name`)
	if err != nil {
		return nil, eris.Wrap(err, "list banks")
	}
	banks, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Bank, error) {
		var b Bank
		err := row.Scan(&b.ID, &b.Country, &b.Code, &b.Name, &b.ShortName, &b.BIC)
		return b, err
	})
	return banks, eris.Wrap(err, "read banks")
}

// Detect returns the register id for an account number, or nil when the number names
// no bank in the register.
func Detect(ctx context.Context, q db.Querier, number *string) (*string, error) {
	if number == nil {
		return nil, nil
	}
	id := CandidateID(*number)
	if id == "" {
		return nil, nil
	}
	return Known(ctx, q, id)
}

// Known returns id when it is in the register, nil otherwise.
func Known(ctx context.Context, q db.Querier, id string) (*string, error) {
	var exists bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM bank WHERE id = $1)`, id).Scan(&exists); err != nil {
		return nil, eris.Wrap(err, "look bank up")
	}
	if !exists {
		return nil, nil
	}
	return &id, nil
}
