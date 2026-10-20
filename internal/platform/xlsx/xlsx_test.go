package xlsx

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadsTheSheetItBuilt(t *testing.T) {
	created := time.Date(2026, 10, 18, 20, 1, 6, 0, time.UTC)
	content, err := Build([][]any{
		{"Číslo úvěru", "Název", "Částka"},
		{"0000000001", "Projekt <A & B>", Number("1234.5")},
		{nil, "", Number("46000")},
	}, created)
	require.NoError(t, err)

	wb, err := Read(content)
	require.NoError(t, err)
	assert.Equal(t, created, wb.Created)
	assert.Equal(t, [][]string{
		{"Číslo úvěru", "Název", "Částka"},
		{"0000000001", "Projekt <A & B>", "1234.5"},
		{"", "", "46000"},
	}, wb.Rows)
	day, err := wb.DateOf("46000")
	require.NoError(t, err)
	assert.Equal(t, time.Date(2025, 12, 9, 0, 0, 0, 0, time.UTC), day)
}

func TestColumnsBeyondZ(t *testing.T) {
	assert.Equal(t, "AA", columnName(26))
	col, err := column("AB7")
	require.NoError(t, err)
	assert.Equal(t, 27, col)
}

func TestRefusesWhatIsNotAWorkbook(t *testing.T) {
	_, err := Read([]byte("Datum;Částka\n"))
	assert.ErrorIs(t, err, ErrNotAWorkbook)
}

func TestRefusesRowsPaddedOutToAFarColumn(t *testing.T) {
	_, err := column("XFD1")
	assert.ErrorIs(t, err, ErrNotAWorkbook, "a column past the limit is refused before any row is padded")

	var sheet strings.Builder
	for range maxCells/maxColumns + 1 {
		sheet.WriteString(`<row><c r="IV1"/></row>`)
	}
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		"xl/workbook.xml":          `<workbook><sheets><sheet r:id="rId1" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"/></sheets></workbook>`,
		"xl/worksheets/sheet1.xml": `<worksheet><sheetData>` + sheet.String() + `</sheetData></worksheet>`,
	} {
		f, err := w.Create(name)
		require.NoError(t, err)
		_, err = f.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())

	_, err = Read(buf.Bytes())
	assert.ErrorIs(t, err, ErrNotAWorkbook)
}

func TestRefusesAnOversizedPart(t *testing.T) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.Create("xl/workbook.xml")
	require.NoError(t, err)
	_, err = f.Write([]byte(strings.Repeat(" ", maxPartBytes+1)))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	_, err = Read(buf.Bytes())
	assert.ErrorIs(t, err, ErrNotAWorkbook)
}

func TestRefusesElementsNestedTooDeep(t *testing.T) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		"xl/workbook.xml":          `<workbook><sheets><sheet r:id="rId1" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"/></sheets></workbook>`,
		"xl/worksheets/sheet1.xml": `<worksheet><sheetData><row>` + strings.Repeat("<a>", 1000) + `</row></sheetData></worksheet>`,
	} {
		f, err := w.Create(name)
		require.NoError(t, err)
		_, err = f.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())

	_, err := Read(buf.Bytes())
	assert.ErrorIs(t, err, ErrNotAWorkbook)
}
