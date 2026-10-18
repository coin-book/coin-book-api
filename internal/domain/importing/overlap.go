package importing

import (
	"context"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/rotisserie/eris"

	"coin-book-api/internal/domain/importing/statement"
	"coin-book-api/internal/platform/db"
)

// otherStatementWindowDays: formats date one payment differently (mBank's CSV the card
// payment, ABO its booking, mostly two days later). Measured: coin-book/importer-formaty.md.
const otherStatementWindowDays = 7

const minNameWordLength = 4

type otherStatementRow struct {
	bookedOn            time.Time
	amountMinor         int64
	counterparty        string
	counterpartyAccount string
	description         string
	taken               bool
}

// markRowsInOtherStatements finds rows the target account already holds from another
// format of the same bank account: their ids differ, so dedupe by id cannot see them.
// One stored row stands for one incoming row, so repeated payments are counted, not merged.
func markRowsInOtherStatements(ctx context.Context, q db.Querier, p *plan, known map[string]storedRow) error {
	for index, acc := range p.accounts {
		if acc.existing == nil {
			continue
		}
		var pending []int
		for i, r := range p.rows {
			if _, isKnown := known[r.id]; r.account == index && !isKnown {
				pending = append(pending, i)
			}
		}
		if len(pending) == 0 {
			continue
		}
		sort.SliceStable(pending, func(a, b int) bool {
			return p.rows[pending[a]].row.BookedOn.Before(p.rows[pending[b]].row.BookedOn)
		})
		from := p.rows[pending[0]].row.BookedOn.AddDate(0, 0, -otherStatementWindowDays)
		to := p.rows[pending[len(pending)-1]].row.BookedOn.AddDate(0, 0, otherStatementWindowDays)
		others, err := rowsFromOtherFormats(ctx, q, acc.existing.ID, p.st.Format, from, to)
		if err != nil {
			return err
		}
		for _, i := range pending {
			if match := closestSamePayment(others, p.rows[i].row); match != nil {
				match.taken = true
				p.rows[i].inOtherStatement = true
			}
		}
	}
	return nil
}

func rowsFromOtherFormats(ctx context.Context, q db.Querier, accountID uuid.UUID, format statement.Format, from, to time.Time) ([]*otherStatementRow, error) {
	rows, err := q.Query(ctx, `
		SELECT t.booked_on, t.amount_minor, coalesce(t.counterparty, ''), coalesce(t.counterparty_account, ''), t.description
		FROM transaction t JOIN import_batch b ON b.id = t.import_batch_id
		WHERE t.account_id = $1 AND b.format <> $2 AND t.booked_on BETWEEN $3 AND $4
		ORDER BY t.booked_on`, accountID, string(format), from, to)
	if err != nil {
		return nil, eris.Wrap(err, "find rows from other statements")
	}
	defer rows.Close()
	var others []*otherStatementRow
	for rows.Next() {
		o := &otherStatementRow{}
		if err := rows.Scan(&o.bookedOn, &o.amountMinor, &o.counterparty, &o.counterpartyAccount, &o.description); err != nil {
			return nil, eris.Wrap(err, "read rows from other statements")
		}
		others = append(others, o)
	}
	return others, eris.Wrap(rows.Err(), "read rows from other statements")
}

func closestSamePayment(others []*otherStatementRow, r statement.Row) *otherStatementRow {
	var best *otherStatementRow
	bestDistance := otherStatementWindowDays + 1
	for _, o := range others {
		if o.taken || o.amountMinor != r.AmountMinor || !samePayee(r, o) {
			continue
		}
		if distance := daysApart(o.bookedOn, r.BookedOn); distance < bestDistance {
			best, bestDistance = o, distance
		}
	}
	return best
}

func samePayee(r statement.Row, o *otherStatementRow) bool {
	if r.CounterpartyAccount != "" && r.CounterpartyAccount == o.counterpartyAccount {
		return true
	}
	return nameAppearsIn(r.Counterparty, o.description) || nameAppearsIn(o.counterparty, r.Description)
}

// nameAppearsIn looks for the name's first real word: formats shorten and reorder
// names ("ROHLIK.CZ" against "Rohlik.cz Praha"), the first word survives.
func nameAppearsIn(name, text string) bool {
	for _, word := range strings.Fields(strings.ToUpper(name)) {
		word = strings.Trim(word, ".,*-/")
		if utf8.RuneCountInString(word) >= minNameWordLength {
			return strings.Contains(strings.ToUpper(text), word)
		}
	}
	return false
}

func daysApart(a, b time.Time) int {
	days := int(a.Sub(b).Hours() / 24)
	if days < 0 {
		return -days
	}
	return days
}
