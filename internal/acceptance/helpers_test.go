//go:build integration

package acceptance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/text/encoding/charmap"

	"coin-book-api/internal/app"
	"coin-book-api/internal/platform/clock"
	"coin-book-api/internal/platform/config"
	"coin-book-api/internal/testutil"
)

var pg *testutil.Postgres

var today = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

const ownMBankAccount = "000019-1234567890/6210"

type client struct {
	t      *testing.T
	server *httptest.Server
	token  string
}

func newClient(t *testing.T) *client {
	t.Helper()
	pg.TruncateAll(t)
	cfg := &config.Config{}
	cfg.Server.AllowedHosts = []string{"127.0.0.1", "localhost"}
	cfg.Server.AuthRateLimitPerMinute = 1000
	cfg.Server.MaxUploadMB = 20
	cfg.JWT.Secret = strings.Repeat("s", 32)
	cfg.JWT.AccessTokenMinutes = 60
	cfg.JWT.RefreshTokenHours = 24
	cfg.Storage.ImportsDir = t.TempDir()

	handler, _ := app.New(cfg, pg.Pool, clock.Fixed{At: today}, app.Build{})
	c := &client{t: t, server: httptest.NewServer(handler)}
	t.Cleanup(c.server.Close)

	var session struct {
		AccessToken string `json:"access_token"`
	}
	c.do(http.MethodPost, "/api/v1/auth/setup", map[string]string{"email": "test@coinbook.local", "password": "heslo-heslo-1"}, http.StatusOK, &session)
	c.token = session.AccessToken
	return c
}

func (c *client) withToken(token string) *client {
	return &client{t: c.t, server: c.server, token: token}
}

func (c *client) do(method, path string, body any, wantStatus int, out any) {
	c.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(c.t, err)
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.server.URL+path, reader)
	require.NoError(c.t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.send(req, wantStatus, out)
}

func (c *client) upload(path, name string, content []byte, wantStatus int, out any) {
	c.t.Helper()
	c.uploadMapped(path, name, content, nil, wantStatus, out)
}

// uploadMapped sends the import with a mapping form field, keyed by accounts[].key.
func (c *client) uploadMapped(path, name string, content []byte, mapping map[string]any, wantStatus int, out any) {
	c.t.Helper()
	var buf bytes.Buffer
	form := multipart.NewWriter(&buf)
	part, err := form.CreateFormFile("file", name)
	require.NoError(c.t, err)
	_, err = part.Write(content)
	require.NoError(c.t, err)
	if mapping != nil {
		raw, err := json.Marshal(mapping)
		require.NoError(c.t, err)
		require.NoError(c.t, form.WriteField("mapping", string(raw)))
	}
	require.NoError(c.t, form.Close())
	req, err := http.NewRequest(http.MethodPost, c.server.URL+path, &buf)
	require.NoError(c.t, err)
	req.Header.Set("Content-Type", form.FormDataContentType())
	c.send(req, wantStatus, out)
}

func (c *client) send(req *http.Request, wantStatus int, out any) {
	c.t.Helper()
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(c.t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(c.t, err)
	require.Equal(c.t, wantStatus, resp.StatusCode, "%s %s: %s", req.Method, req.URL.Path, raw)
	if out != nil && len(raw) > 0 {
		require.NoError(c.t, json.Unmarshal(raw, out), string(raw))
	}
}

type row struct {
	day         string
	description string
	bankCat     string
	minor       int64
}

func decimal(minor int64) string {
	sign := ""
	if minor < 0 {
		sign, minor = "-", -minor
	}
	return fmt.Sprintf("%s%d,%02d", sign, minor/100, minor%100)
}

func mbankStatement(t *testing.T, account string, rows []row) []byte {
	t.Helper()
	var income, expense int64
	for _, r := range rows {
		if r.minor > 0 {
			income += r.minor
		} else {
			expense += r.minor
		}
	}
	var b strings.Builder
	line := func(s string) { b.WriteString(s + "\r\n") }
	line("#Klient;")
	line("#Za období:;")
	line("01.08.2026;16.10.2026;")
	line("      Test Klient - " + account + ";")
	line("      #Měna;#Příjmy;#Výdaje;")
	line("CZK;" + decimal(income) + ";" + decimal(expense) + ";")
	line("#Datum operace;#Popis operace;#Účet;#Kategorie;#Částka;")
	for _, r := range rows {
		line(fmt.Sprintf("%s;%-98s;mKonto Test;%s;%s CZK;;", r.day, r.description, r.bankCat, decimal(r.minor)))
	}
	encoded, err := charmap.Windows1250.NewEncoder().String(b.String())
	require.NoError(t, err)
	return []byte(encoded)
}

func revolutStatement(openingMinor int64, rows []row) []byte {
	var b bytes.Buffer
	b.WriteString("Bežné účty Výpisy o transakciách\n\nOsobný účet (CZK)\n\nVýpis transakcií\n")
	b.WriteString("Dátum,Popis,Kategória,Pohyb peňazí,Zostatok,Zrazená daň,Iné dane,Poplatky\n")
	balance, sum := openingMinor, int64(0)
	for _, r := range rows {
		balance += r.minor
		sum += r.minor
		d, _ := time.Parse(time.DateOnly, r.day)
		fmt.Fprintf(&b, "%d. %d. %d,%s,%s,\"%s CZK\",\"%s CZK\",\"0,00 CZK\",\"0,00 CZK\",\"0,00 CZK\"\n",
			d.Day(), int(d.Month()), d.Year(), r.description, r.bankCat, decimal(r.minor), decimal(balance))
	}
	fmt.Fprintf(&b, "Spolu,,,\"%s CZK\",,,,\n---------\n", decimal(sum))
	return b.Bytes()
}

type importAccount struct {
	Key          string  `json:"key"`
	AccountID    *string `json:"account_id"`
	IsNew        bool    `json:"is_new"`
	MatchedBy    string  `json:"matched_by"`
	NeedsMapping bool    `json:"needs_mapping"`
	New          int     `json:"new"`
	Duplicates   int     `json:"duplicates"`
}

type importResult struct {
	Accounts         []importAccount `json:"accounts"`
	CategorizedFile  int             `json:"categorized_by_file"`
	BatchID          string          `json:"batch_id"`
	ChecksPassed     bool            `json:"checks_passed"`
	AlreadyImported  bool            `json:"already_imported"`
	RowsTotal        int             `json:"rows_total"`
	RowsNew          int             `json:"rows_new"`
	RowsDuplicate    int             `json:"rows_duplicate"`
	TransfersMatched int             `json:"transfers_matched"`
	CategorizedRule  int             `json:"categorized_by_rule"`
	CategorizedBank  int             `json:"categorized_by_bank"`
}

type transactionList struct {
	Transactions []struct {
		ID         string `json:"id"`
		BookedOn   string `json:"booked_on"`
		SourceLine int    `json:"source_line"`
		Categories []struct {
			CategoryID string `json:"category_id"`
			Source     string `json:"source"`
		} `json:"categories"`
		Transfer *struct {
			CounterpartTxID string `json:"counterpart_tx_id"`
		} `json:"transfer"`
		Counterparty string `json:"counterparty"`
	} `json:"transactions"`
	NextCursor string `json:"next_cursor"`
}

func (c *client) transactions(query string) transactionList {
	c.t.Helper()
	var list transactionList
	c.do(http.MethodGet, "/api/v1/transactions?limit=200&"+query, nil, http.StatusOK, &list)
	return list
}

func (c *client) categoryID(code string) string {
	c.t.Helper()
	var taxonomy struct {
		Categories []struct {
			ID   string `json:"id"`
			Code string `json:"code"`
		} `json:"categories"`
	}
	c.do(http.MethodGet, "/api/v1/categories", nil, http.StatusOK, &taxonomy)
	for _, cat := range taxonomy.Categories {
		if cat.Code == code {
			return cat.ID
		}
	}
	c.t.Fatalf("no category %s", code)
	return ""
}
