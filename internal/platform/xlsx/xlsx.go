// Package xlsx reads the first sheet of an Office Open XML workbook as rows of cell texts,
// enough for bank and platform exports; it evaluates nothing and keeps no formatting.
package xlsx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/rotisserie/eris"
)

// A workbook part larger than this is refused rather than inflated: an export of a few
// thousand rows is well under a megabyte; a zip bomb, or a part full of empty elements, is not.
const maxPartBytes = 4 << 20

// Exports are tables of tens of columns and thousands of rows. Rows are padded out to their
// last cell, so a few bytes naming a far column could otherwise take gigabytes.
const (
	// maxDepth: a workbook nests a handful of levels; the decoder keeps every open element.
	maxDepth   = 16
	maxColumns = 256
	maxRows    = 100_000
	maxCells   = 2_000_000
)

var ErrNotAWorkbook = eris.New("xlsx: not a workbook")

type Workbook struct {
	// Rows of the first sheet; a missing cell is "". Numbers and dates are their stored text.
	Rows [][]string
	// Created is when the file was written, from its document properties; zero when absent.
	Created time.Time
	// Date1904: date serials count from 1904 rather than 1900.
	Date1904 bool
}

func Read(content []byte) (*Workbook, error) {
	z, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return nil, eris.Wrap(ErrNotAWorkbook, err.Error())
	}
	parts := map[string]*zip.File{}
	for _, f := range z.File {
		parts[f.Name] = f
	}
	wb := &Workbook{}
	sheet, err := firstSheet(parts, wb)
	if err != nil {
		return nil, err
	}
	var shared []string
	if f, ok := parts["xl/sharedStrings.xml"]; ok {
		if shared, err = sharedStrings(f); err != nil {
			return nil, err
		}
	}
	if wb.Rows, err = rows(sheet, shared); err != nil {
		return nil, err
	}
	if f, ok := parts["docProps/core.xml"]; ok {
		wb.Created, _ = created(f)
	}
	return wb, nil
}

// DateOf turns a date serial into the calendar day it stands for.
func (w *Workbook) DateOf(serial string) (time.Time, error) {
	days, err := strconv.ParseFloat(strings.TrimSpace(serial), 64)
	if err != nil || days < 1 || days > 2_958_465 {
		return time.Time{}, eris.Errorf("xlsx: %q is not a date serial", serial)
	}
	epoch := time.Date(1899, time.December, 30, 0, 0, 0, 0, time.UTC)
	if w.Date1904 {
		epoch = time.Date(1904, time.January, 1, 0, 0, 0, 0, time.UTC)
	}
	return epoch.AddDate(0, 0, int(days)), nil
}

func open(f *zip.File) ([]byte, error) {
	if f.UncompressedSize64 > maxPartBytes {
		return nil, eris.Wrapf(ErrNotAWorkbook, "%s is too large", f.Name)
	}
	r, err := f.Open()
	if err != nil {
		return nil, eris.Wrap(ErrNotAWorkbook, err.Error())
	}
	defer func() { _ = r.Close() }()
	// The declared size can lie; never read past the limit whatever it says.
	data, err := io.ReadAll(io.LimitReader(r, maxPartBytes+1))
	if err != nil {
		return nil, eris.Wrap(ErrNotAWorkbook, err.Error())
	}
	if len(data) > maxPartBytes {
		return nil, eris.Wrapf(ErrNotAWorkbook, "%s is too large", f.Name)
	}
	return data, nil
}

func decoder(f *zip.File) (*xml.Decoder, error) {
	data, err := open(f)
	if err != nil {
		return nil, err
	}
	return xml.NewDecoder(bytes.NewReader(data)), nil
}

func decode(f *zip.File, v any) error {
	data, err := open(f)
	if err != nil {
		return err
	}
	return eris.Wrap(xml.Unmarshal(data, v), "xlsx: read "+f.Name)
}

type workbookXML struct {
	Properties struct {
		Date1904 string `xml:"date1904,attr"`
	} `xml:"workbookPr"`
	Sheets []struct {
		RelID string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
	} `xml:"sheets>sheet"`
}

type relationshipsXML struct {
	Relationships []struct {
		ID     string `xml:"Id,attr"`
		Target string `xml:"Target,attr"`
	} `xml:"Relationship"`
}

func firstSheet(parts map[string]*zip.File, wb *Workbook) (*zip.File, error) {
	f, ok := parts["xl/workbook.xml"]
	if !ok {
		return nil, eris.Wrap(ErrNotAWorkbook, "no xl/workbook.xml")
	}
	var book workbookXML
	if err := decode(f, &book); err != nil {
		return nil, err
	}
	wb.Date1904 = book.Properties.Date1904 == "1" || book.Properties.Date1904 == "true"
	if len(book.Sheets) == 0 {
		return nil, eris.Wrap(ErrNotAWorkbook, "no sheet")
	}
	target := "worksheets/sheet1.xml"
	if rels, ok := parts["xl/_rels/workbook.xml.rels"]; ok {
		var r relationshipsXML
		if err := decode(rels, &r); err != nil {
			return nil, err
		}
		for _, rel := range r.Relationships {
			if rel.ID == book.Sheets[0].RelID {
				target = rel.Target
			}
		}
	}
	name := path.Clean(path.Join("xl", target))
	if strings.HasPrefix(target, "/") {
		name = strings.TrimPrefix(path.Clean(target), "/")
	}
	sheet, ok := parts[name]
	if !ok {
		return nil, eris.Wrapf(ErrNotAWorkbook, "no %s", name)
	}
	return sheet, nil
}

type richText struct {
	T    string `xml:"t"`
	Runs []struct {
		T string `xml:"t"`
	} `xml:"r"`
}

func (r richText) text() string {
	if len(r.Runs) == 0 {
		return r.T
	}
	var b strings.Builder
	for _, run := range r.Runs {
		b.WriteString(run.T)
	}
	return b.String()
}

// sharedStrings and rows decode one element at a time and count as they go: unmarshalling a
// whole part first would let a few kilobytes of compressed empty cells take gigabytes.
func sharedStrings(f *zip.File) ([]string, error) {
	d, err := decoder(f)
	if err != nil {
		return nil, err
	}
	var out []string
	var depth nesting
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, eris.Wrap(err, "xlsx: read "+f.Name)
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "si" {
			if err := depth.see(tok); err != nil {
				return nil, err
			}
			continue
		}
		if len(out) >= maxCells {
			return nil, eris.Wrapf(ErrNotAWorkbook, "more than %d shared strings", maxCells)
		}
		var si richText
		if err := d.DecodeElement(&si, &start); err != nil {
			return nil, eris.Wrap(err, "xlsx: read "+f.Name)
		}
		out = append(out, si.text())
	}
}

// nesting counts the elements open around the decoder's position; the decoder keeps a record of
// every one, so a few kilobytes of unclosed tags would otherwise take gigabytes.
type nesting int

func (n *nesting) see(tok xml.Token) error {
	switch tok.(type) {
	case xml.StartElement:
		*n++
		if *n > maxDepth {
			return eris.Wrapf(ErrNotAWorkbook, "elements nested deeper than %d", maxDepth)
		}
	case xml.EndElement:
		*n--
	}
	return nil
}

type cellXML struct {
	Ref    string   `xml:"r,attr"`
	Type   string   `xml:"t,attr"`
	Value  string   `xml:"v"`
	Inline richText `xml:"is"`
}

func rows(f *zip.File, shared []string) ([][]string, error) {
	d, err := decoder(f)
	if err != nil {
		return nil, err
	}
	var out [][]string
	cells := 0
	var depth nesting
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, eris.Wrap(err, "xlsx: read "+f.Name)
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "c" {
			if err := depth.see(tok); err != nil {
				return nil, err
			}
		}
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "row":
			if len(out) >= maxRows {
				return nil, eris.Wrapf(ErrNotAWorkbook, "more than %d rows", maxRows)
			}
			out = append(out, nil)
		case "c":
			if len(out) == 0 {
				return nil, eris.Wrap(ErrNotAWorkbook, "a cell outside a row")
			}
			var c cellXML
			if err := d.DecodeElement(&c, &start); err != nil {
				return nil, eris.Wrap(err, "xlsx: read "+f.Name)
			}
			row := out[len(out)-1]
			col := len(row)
			if c.Ref != "" {
				if col, err = column(c.Ref); err != nil {
					return nil, err
				}
			}
			if col+1 > len(row) {
				cells += col + 1 - len(row)
				if cells > maxCells {
					return nil, eris.Wrapf(ErrNotAWorkbook, "more than %d cells", maxCells)
				}
				row = append(row, make([]string, col+1-len(row))...)
			}
			if row[col], err = cellText(c, shared); err != nil {
				return nil, err
			}
			out[len(out)-1] = row
		}
	}
}

func cellText(c cellXML, shared []string) (string, error) {
	switch c.Type {
	case "s":
		i, err := strconv.Atoi(strings.TrimSpace(c.Value))
		if err != nil || i < 0 || i >= len(shared) {
			return "", eris.Wrapf(ErrNotAWorkbook, "cell %s points at no shared string", c.Ref)
		}
		return shared[i], nil
	case "inlineStr":
		return c.Inline.text(), nil
	default:
		return c.Value, nil
	}
}

// column is the zero-based column of a reference such as "AB12".
func column(ref string) (int, error) {
	col := 0
	letters := 0
	for _, r := range ref {
		if r < 'A' || r > 'Z' {
			break
		}
		col = col*26 + int(r-'A'+1)
		letters++
		if col > maxColumns {
			return 0, eris.Wrapf(ErrNotAWorkbook, "cell %s is out of range", ref)
		}
	}
	if letters == 0 {
		return 0, eris.Wrapf(ErrNotAWorkbook, "cell reference %q", ref)
	}
	return col - 1, nil
}

func created(f *zip.File) (time.Time, error) {
	var core struct {
		Created string `xml:"http://purl.org/dc/terms/ created"`
	}
	if err := decode(f, &core); err != nil {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339, strings.TrimSpace(core.Created))
}
