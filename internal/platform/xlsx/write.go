package xlsx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/rotisserie/eris"
)

// Number is a cell stored as a number; any other cell value is stored as its text.
type Number string

// Build writes a one-sheet workbook shaped like the exports Apache POI produces, for
// synthetic fixtures and demo data.
func Build(sheet [][]any, created time.Time) ([]byte, error) {
	var shared []string
	index := map[string]int{}
	var data strings.Builder
	for r, row := range sheet {
		fmt.Fprintf(&data, `<row r="%d">`, r+1)
		for c, v := range row {
			ref := columnName(c) + strconv.Itoa(r+1)
			switch v := v.(type) {
			case nil:
			case Number:
				fmt.Fprintf(&data, `<c r="%s"><v>%s</v></c>`, ref, escape(string(v)))
			default:
				text := fmt.Sprint(v)
				i, ok := index[text]
				if !ok {
					i = len(shared)
					index[text] = i
					shared = append(shared, text)
				}
				fmt.Fprintf(&data, `<c r="%s" t="s"><v>%d</v></c>`, ref, i)
			}
		}
		data.WriteString(`</row>`)
	}
	var sst strings.Builder
	for _, s := range shared {
		fmt.Fprintf(&sst, `<si><t>%s</t></si>`, escape(s))
	}
	const ns = `xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"`
	parts := []struct{ name, body string }{
		{"[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/><Override PartName="/xl/sharedStrings.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sharedStrings+xml"/><Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/></Types>`},
		{"_rels/.rels", `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/></Relationships>`},
		{"docProps/core.xml", `<?xml version="1.0" encoding="UTF-8"?><cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"><dcterms:created xsi:type="dcterms:W3CDTF">` + created.UTC().Format(time.RFC3339) + `</dcterms:created><dc:creator>Apache POI</dc:creator></cp:coreProperties>`},
		{"xl/workbook.xml", `<?xml version="1.0" encoding="UTF-8"?><workbook ` + ns + ` xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Sheet0" sheetId="1" r:id="rId3"/></sheets></workbook>`},
		{"xl/_rels/workbook.xml.rels", `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings" Target="sharedStrings.xml"/><Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`},
		{"xl/sharedStrings.xml", `<?xml version="1.0" encoding="UTF-8"?><sst ` + ns + ` count="` + strconv.Itoa(len(shared)) + `">` + sst.String() + `</sst>`},
		{"xl/worksheets/sheet1.xml", `<?xml version="1.0" encoding="UTF-8"?><worksheet ` + ns + `><sheetData>` + data.String() + `</sheetData></worksheet>`},
	}
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, p := range parts {
		f, err := w.CreateHeader(&zip.FileHeader{Name: p.name, Method: zip.Deflate, Modified: created})
		if err != nil {
			return nil, eris.Wrap(err, "xlsx: write part")
		}
		if _, err := f.Write([]byte(p.body)); err != nil {
			return nil, eris.Wrap(err, "xlsx: write part")
		}
	}
	if err := w.Close(); err != nil {
		return nil, eris.Wrap(err, "xlsx: close workbook")
	}
	return buf.Bytes(), nil
}

func columnName(i int) string {
	name := ""
	for i++; i > 0; i = (i - 1) / 26 {
		name = string(rune('A'+(i-1)%26)) + name
	}
	return name
}

func escape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
