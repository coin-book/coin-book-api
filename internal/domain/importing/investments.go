package importing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/rotisserie/eris"
	"github.com/shopspring/decimal"

	"coin-book-api/internal/domain/importing/statement"
	"coin-book-api/internal/domain/investment"
	"coin-book-api/internal/platform/db"
	"coin-book-api/internal/platform/money"
)

type investmentRecord struct {
	id      string
	account int
	known   bool
}

// plannedInvestments holds the ids of one account's records, in statement order.
type plannedInvestments struct {
	account   int
	src       *statement.Investments
	buys      []investmentRecord
	dividends []investmentRecord
	lotSales  []investmentRecord
}

// investmentIDs hash like transactionIDs; every identity key starts with the record kind.
func investmentIDs(keys []string, owner, identity string, seen map[string]int) []string {
	ids := make([]string, len(keys))
	for i, k := range keys {
		key := owner + "|" + identity + "|" + k
		occurrence := seen[key]
		seen[key]++
		sum := sha256.Sum256([]byte(key + "|" + strconv.Itoa(occurrence)))
		ids[i] = "inv_" + hex.EncodeToString(sum[:16])
	}
	return ids
}

func planInvestments(src *statement.Investments, account int, owner, identity string) *plannedInvestments {
	seen := map[string]int{}
	records := func(ids []string) []investmentRecord {
		out := make([]investmentRecord, len(ids))
		for i, id := range ids {
			out[i] = investmentRecord{id: id, account: account}
		}
		return out
	}
	buyKeys := make([]string, len(src.Buys))
	for i, b := range src.Buys {
		buyKeys[i] = b.IdentityKey
	}
	dividendKeys := make([]string, len(src.Dividends))
	for i, d := range src.Dividends {
		dividendKeys[i] = d.IdentityKey
	}
	lotKeys := make([]string, len(src.LotSales))
	for i, l := range src.LotSales {
		lotKeys[i] = l.IdentityKey
	}
	return &plannedInvestments{
		account:   account,
		src:       src,
		buys:      records(investmentIDs(buyKeys, owner, identity, seen)),
		dividends: records(investmentIDs(dividendKeys, owner, identity, seen)),
		lotSales:  records(investmentIDs(lotKeys, owner, identity, seen)),
	}
}

func (p *plannedInvestments) all() []*investmentRecord {
	out := make([]*investmentRecord, 0, len(p.buys)+len(p.dividends)+len(p.lotSales))
	for _, list := range [][]investmentRecord{p.buys, p.dividends, p.lotSales} {
		for i := range list {
			out = append(out, &list[i])
		}
	}
	return out
}

// markKnownInvestments flags records an earlier import already wrote.
func markKnownInvestments(ctx context.Context, q db.Querier, planned []*plannedInvestments) error {
	var ids []string
	for _, p := range planned {
		for _, r := range p.all() {
			ids = append(ids, r.id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := q.Query(ctx, `
		SELECT id FROM investment_tx WHERE id = ANY($1)
		UNION ALL SELECT id FROM investment_lot_sale WHERE id = ANY($1)`, ids)
	if err != nil {
		return eris.Wrap(err, "find existing investment records")
	}
	found, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return eris.Wrap(err, "read existing investment records")
	}
	known := make(map[string]bool, len(found))
	for _, id := range found {
		known[id] = true
	}
	for _, p := range planned {
		for _, r := range p.all() {
			r.known = known[r.id]
		}
	}
	return nil
}

type instrumentSeen struct {
	security statement.Security
	// currency is the first trade's; it only guesses the exchange for the Yahoo symbol.
	currency money.Currency
}

func (s *instrumentSeen) see(sec statement.Security, currency money.Currency) {
	if s.security.Ticker == "" {
		s.security.Ticker = sec.Ticker
	}
	if s.security.Name == "" {
		s.security.Name = sec.Name
	}
	if s.security.YahooSymbol == "" {
		s.security.YahooSymbol = sec.YahooSymbol
	}
	if s.currency == "" {
		s.currency = currency
	}
}

func instrumentsOf(planned []*plannedInvestments) []*instrumentSeen {
	byISIN := map[string]*instrumentSeen{}
	var order []*instrumentSeen
	see := func(sec statement.Security, currency money.Currency) {
		inst, ok := byISIN[sec.ISIN]
		if !ok {
			inst = &instrumentSeen{security: statement.Security{ISIN: sec.ISIN}}
			byISIN[sec.ISIN] = inst
			order = append(order, inst)
		}
		inst.see(sec, currency)
	}
	for _, p := range planned {
		for _, l := range p.src.LotSales {
			see(l.Security, l.Currency)
		}
		for _, b := range p.src.Buys {
			see(b.Security, b.Currency)
		}
		for _, d := range p.src.Dividends {
			see(d.Security, d.Currency)
		}
	}
	return order
}

// upsertInstruments records instruments in the shared register. What one statement
// leaves out (a dividend names no ticker) another fills in; nothing is ever replaced,
// so one user's import cannot rewrite what another holder sees. The Yahoo symbol is set by the
// first import only (ADR 209): a later one could otherwise pick any series for a missing one.
func upsertInstruments(ctx context.Context, tx pgx.Tx, planned []*plannedInvestments) error {
	instruments := instrumentsOf(planned)
	// One order for every import, so two users' imports cannot lock shared rows crosswise.
	slices.SortFunc(instruments, func(a, b *instrumentSeen) int { return strings.Compare(a.security.ISIN, b.security.ISIN) })
	batch := &pgx.Batch{}
	for _, inst := range instruments {
		yahoo := investment.YahooSymbol(inst.security.Ticker, inst.currency)
		if inst.security.YahooSymbol != "" {
			yahoo = &inst.security.YahooSymbol
		}
		batch.Queue(`
			INSERT INTO instrument (isin, ticker, name, yahoo_symbol) VALUES ($1, nullif($2, ''), $3, $4)
			ON CONFLICT (isin) DO UPDATE SET
				ticker = coalesce(instrument.ticker, excluded.ticker),
				name = CASE WHEN instrument.name = '' THEN excluded.name ELSE instrument.name END,
				yahoo_symbol = instrument.yahoo_symbol`,
			inst.security.ISIN, inst.security.Ticker, inst.security.Name, yahoo)
	}
	return eris.Wrap(tx.SendBatch(ctx, batch).Close(), "record instruments")
}

func writeInvestments(ctx context.Context, tx pgx.Tx, planned []*plannedInvestments, accountIDs []uuid.UUID, batchID uuid.UUID) error {
	if len(planned) == 0 {
		return nil
	}
	if err := upsertInstruments(ctx, tx, planned); err != nil {
		return err
	}
	var txRows, lotRows [][]any
	for _, p := range planned {
		accountID := accountIDs[p.account]
		for i, b := range p.src.Buys {
			if !p.buys[i].known {
				txRows = append(txRows, []any{p.buys[i].id, accountID, b.Security.ISIN, "buy", b.TradedOn, numeric(b.Quantity),
					string(b.Currency), b.GrossMinor, b.NetMinor, b.GrossCZKMinor, b.NetCZKMinor, b.FeeCZKMinor, b.TaxCZKMinor,
					batchID, b.Line})
			}
		}
		for i, d := range p.src.Dividends {
			if !p.dividends[i].known {
				txRows = append(txRows, []any{p.dividends[i].id, accountID, d.Security.ISIN, "dividend", d.PaidOn, nil,
					string(d.Currency), d.GrossMinor, d.NetMinor, d.GrossCZKMinor, d.NetCZKMinor, d.FeeCZKMinor, d.TaxCZKMinor,
					batchID, d.Line})
			}
		}
		for i, l := range p.src.LotSales {
			if !p.lotSales[i].known {
				lotRows = append(lotRows, []any{p.lotSales[i].id, accountID, l.Security.ISIN, l.BoughtOn, l.SoldOn,
					numeric(l.Quantity), string(l.Currency), l.ProceedsMinor, l.CostMinor, l.GainMinor, l.ProceedsCZKMinor,
					l.CostCZKMinor, l.GainCZKMinor, l.FeeCZKMinor, l.TaxCZKMinor, batchID, l.Line})
			}
		}
	}
	if err := copyThroughStaging(ctx, tx, "investment_tx", []string{"id", "account_id", "isin", "kind", "traded_on",
		"quantity", "currency", "gross_minor", "net_minor", "gross_czk_minor", "net_czk_minor", "fee_czk_minor", "tax_czk_minor",
		"import_batch_id", "source_line"}, txRows); err != nil {
		return eris.Wrap(err, "insert investment transactions")
	}
	err := copyThroughStaging(ctx, tx, "investment_lot_sale", []string{"id", "account_id", "isin", "bought_on", "sold_on",
		"quantity", "currency", "proceeds_minor", "cost_minor", "gain_minor", "proceeds_czk_minor", "cost_czk_minor",
		"gain_czk_minor", "fee_czk_minor", "tax_czk_minor", "import_batch_id", "source_line"}, lotRows)
	return eris.Wrap(err, "insert sold lots")
}

// copyThroughStaging bulk-inserts into a table under row-level security, which COPY
// refuses: the rows go to a temporary copy first, then across with INSERT … SELECT.
func copyThroughStaging(ctx context.Context, tx pgx.Tx, table string, columns []string, rows [][]any) error {
	if len(rows) == 0 {
		return nil
	}
	staging := table + "_staging"
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE `+staging+` (LIKE `+table+` INCLUDING DEFAULTS) ON COMMIT DROP`); err != nil {
		return eris.Wrap(err, "create staging table")
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{staging}, columns, pgx.CopyFromRows(rows)); err != nil {
		return eris.Wrap(err, "copy to staging table")
	}
	_, err := tx.Exec(ctx, `INSERT INTO `+table+` SELECT * FROM `+staging)
	return eris.Wrap(err, "insert from staging table")
}

func numeric(d decimal.Decimal) pgtype.Numeric {
	var n pgtype.Numeric
	_ = n.Scan(d.String())
	return n
}

func newRecords(planned []*plannedInvestments) bool {
	for _, p := range planned {
		for _, r := range p.all() {
			if !r.known {
				return true
			}
		}
	}
	return false
}
