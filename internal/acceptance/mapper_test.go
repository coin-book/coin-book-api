//go:build integration

package acceptance

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const otherBankCSV = "Výpis z účtu;;;;\n" +
	"Období:;01.10.2026 - 31.10.2026;;;\n" +
	"Datum zaúčtování;Název protiúčtu;Zpráva pro příjemce;Příjem;Výdaj\n" +
	"02.10.2026;ROHLIK.CZ;Nákup;;\"1 249,50\"\n" +
	"05.10.2026;FIRMA TEST;Výplata;\"62 000,00\";\n"

const otherBankNovemberCSV = "Výpis z účtu;;;;\n" +
	"Období:;01.11.2026 - 30.11.2026;;;\n" +
	"Datum zaúčtování;Název protiúčtu;Zpráva pro příjemce;Příjem;Výdaj\n" +
	"05.10.2026;FIRMA TEST;Výplata;\"62 000,00\";\n" +
	"03.11.2026;KAVARNA;;;89,00\n"

type inspection struct {
	Rows      [][]string     `json:"rows"`
	Suggested map[string]any `json:"suggested"`
	Template  *struct {
		Name string `json:"name"`
	} `json:"template"`
}

func TestUnknownBankCSVIsMappedOnceThenRecognised(t *testing.T) {
	c := newClient(t)
	c.upload("/api/v1/imports/preview", "banka.csv", []byte(otherBankCSV), http.StatusUnprocessableEntity, nil)

	var inspected inspection
	c.upload("/api/v1/imports/inspect", "banka.csv", []byte(otherBankCSV), http.StatusOK, &inspected)
	assert.Len(t, inspected.Rows, 5)
	assert.EqualValues(t, 2, inspected.Suggested["header_row"])
	layout, err := json.Marshal(inspected.Suggested)
	require.NoError(t, err)

	var preview importResult
	c.uploadForm("/api/v1/imports/preview", "banka.csv", []byte(otherBankCSV),
		map[string]string{"layout": string(layout)}, http.StatusOK, &preview)
	assert.Equal(t, "mapped-csv", preview.Format)
	assert.Equal(t, 2, preview.RowsNew)

	var done importResult
	c.uploadForm("/api/v1/imports", "banka.csv", []byte(otherBankCSV),
		map[string]string{"layout": string(layout), "template_name": "Jiná banka"}, http.StatusCreated, &done)
	require.NotNil(t, done.Template)
	assert.Equal(t, "Jiná banka", done.Template.Name)

	var next importResult
	c.upload("/api/v1/imports", "banka-listopad.csv", []byte(otherBankNovemberCSV), http.StatusCreated, &next)
	require.NotNil(t, next.Template, "the saved template reads the next export without the mapper")
	assert.Equal(t, 1, next.RowsNew)
	assert.Equal(t, 1, next.RowsDuplicate)
	assert.Len(t, c.accounts(), 1, "the next export lands in the same account")

	var reinspected inspection
	c.upload("/api/v1/imports/inspect", "banka-listopad.csv", []byte(otherBankNovemberCSV), http.StatusOK, &reinspected)
	require.NotNil(t, reinspected.Template)

	var templates struct {
		Templates []struct {
			ID string `json:"id"`
		} `json:"templates"`
	}
	c.do(http.MethodGet, "/api/v1/imports/templates", nil, http.StatusOK, &templates)
	require.Len(t, templates.Templates, 1)
	c.do(http.MethodDelete, "/api/v1/imports/templates/"+templates.Templates[0].ID, nil, http.StatusNoContent, nil)
	c.upload("/api/v1/imports/preview", "banka-listopad.csv", []byte(otherBankNovemberCSV), http.StatusUnprocessableEntity, nil)
}

func TestLayoutThatDoesNotFitTheFileIsRefused(t *testing.T) {
	c := newClient(t)
	layout := `{"encoding":"utf-8","delimiter":";","header_row":2,"columns":{"date":1,"amount":0,"description":2},"date_order":"dmy","currency":"CZK"}`
	c.uploadForm("/api/v1/imports/preview", "banka.csv", []byte(otherBankCSV),
		map[string]string{"layout": layout}, http.StatusUnprocessableEntity, nil)

	incomplete := `{"encoding":"utf-8","delimiter":";","header_row":2,"columns":{"date":0},"date_order":"dmy","currency":"CZK"}`
	c.uploadForm("/api/v1/imports/preview", "banka.csv", []byte(otherBankCSV),
		map[string]string{"layout": incomplete}, http.StatusUnprocessableEntity, nil)
}
