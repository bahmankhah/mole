package content

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
)

// maxDOCXPart caps how much of one XML part of a DOCX file is read, so a
// small compressed file cannot expand without bound.
const maxDOCXPart = 64 << 20

// docxBlock is a paragraph, heading, list item or table of a Word document.
type docxBlock struct {
	heading int // 1-6, 0 for body text
	list    bool
	text    string
	rows    [][]string // set for tables
}

// convertDOCX reads the main document part of a Word (.docx) file.
func convertDOCX(body []byte) (*Document, error) {
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return nil, fmt.Errorf("docx: %w", err)
	}
	files := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		files[f.Name] = f
	}
	mainName := "word/document.xml"
	if rels, ok := files["_rels/.rels"]; ok {
		if raw, err := readZipPart(rels); err == nil {
			if target := officeDocumentTarget(raw); target != "" {
				mainName = target
			}
		}
	}
	main, ok := files[mainName]
	if !ok {
		return nil, fmt.Errorf("docx: %s not found (not a Word document)", mainName)
	}
	raw, err := readZipPart(main)
	if err != nil {
		return nil, fmt.Errorf("docx: %w", err)
	}
	blocks, err := parseDOCXBody(raw)
	if err != nil {
		return nil, fmt.Errorf("docx: %w", err)
	}

	doc := &Document{Pages: 1, Metadata: map[string]interface{}{}}
	doc.Markdown, doc.Text = renderDOCX(blocks)
	if core, ok := files["docProps/core.xml"]; ok {
		if raw, err := readZipPart(core); err == nil {
			applyCoreProperties(doc, raw)
		}
	}
	if doc.Title == "" {
		for _, b := range blocks {
			if b.heading > 0 && b.text != "" {
				doc.Title = firstLine(b.text, 200)
				break
			}
		}
	}
	return doc, nil
}

func readZipPart(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	raw, err := io.ReadAll(io.LimitReader(rc, maxDOCXPart+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxDOCXPart {
		return nil, fmt.Errorf("%s is larger than %d MB", f.Name, maxDOCXPart>>20)
	}
	return raw, nil
}

// officeDocumentTarget finds the main document part in the package relationships.
func officeDocumentTarget(raw []byte) string {
	var rels struct {
		Items []struct {
			Type   string `xml:"Type,attr"`
			Target string `xml:"Target,attr"`
		} `xml:"Relationship"`
	}
	if xml.Unmarshal(raw, &rels) != nil {
		return ""
	}
	for _, r := range rels.Items {
		if strings.HasSuffix(r.Type, "/officeDocument") && r.Target != "" {
			return strings.TrimPrefix(path.Clean("/"+r.Target), "/")
		}
	}
	return ""
}

// isWordML reports whether an element is in the WordprocessingML namespace
// (transitional or strict).
func isWordML(n xml.Name) bool { return strings.Contains(n.Space, "wordprocessingml") }

func wordAttr(e xml.StartElement, local string) string {
	for _, a := range e.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

// headingLevel maps a paragraph style id such as "Heading2" or "Title" to a level.
func headingLevel(style string) int {
	s := strings.ToLower(style)
	switch {
	case s == "title":
		return 1
	case s == "subtitle":
		return 2
	case strings.HasPrefix(s, "heading"):
		if n, err := strconv.Atoi(strings.TrimPrefix(s, "heading")); err == nil && n >= 1 {
			return min(n, 6)
		}
	}
	return 0
}

// parseDOCXBody walks document.xml. Text inside nested tables and text boxes
// is flattened into the enclosing cell or paragraph.
func parseDOCXBody(raw []byte) ([]docxBlock, error) {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	var (
		blocks     []docxBlock
		para       strings.Builder
		cur        docxBlock
		paraDepth  int
		runDepth   int
		inText     bool
		tableDepth int
		rows       [][]string
		row        []string
		cell       strings.Builder
	)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if !isWordML(t.Name) {
				continue
			}
			switch t.Name.Local {
			case "tbl":
				tableDepth++
				if tableDepth == 1 {
					rows = nil
				}
			case "tr":
				if tableDepth == 1 {
					row = nil
				}
			case "tc":
				if tableDepth == 1 {
					cell.Reset()
				}
			case "p":
				paraDepth++
				if paraDepth == 1 {
					para.Reset()
					cur = docxBlock{}
				} else if para.Len() > 0 {
					para.WriteByte(' ')
				}
			case "pStyle":
				if paraDepth == 1 && cur.heading == 0 {
					cur.heading = headingLevel(wordAttr(t, "val"))
				}
			case "outlineLvl":
				if paraDepth == 1 && cur.heading == 0 {
					if n, err := strconv.Atoi(wordAttr(t, "val")); err == nil && n >= 0 && n < 6 {
						cur.heading = n + 1
					}
				}
			case "numPr":
				if paraDepth == 1 {
					cur.list = true
				}
			case "r":
				runDepth++
			case "t":
				inText = runDepth > 0
			case "tab":
				if runDepth > 0 {
					para.WriteByte('\t')
				}
			case "br", "cr":
				if runDepth > 0 {
					para.WriteByte('\n')
				}
			case "noBreakHyphen":
				if runDepth > 0 {
					para.WriteByte('-')
				}
			}
		case xml.CharData:
			if inText {
				para.Write(t)
			}
		case xml.EndElement:
			if !isWordML(t.Name) {
				continue
			}
			switch t.Name.Local {
			case "t":
				inText = false
			case "r":
				if runDepth > 0 {
					runDepth--
				}
			case "p":
				if paraDepth == 0 {
					continue
				}
				paraDepth--
				if paraDepth > 0 {
					continue
				}
				text := strings.TrimSpace(para.String())
				if text == "" {
					continue
				}
				if tableDepth > 0 {
					if cell.Len() > 0 {
						cell.WriteByte(' ')
					}
					cell.WriteString(text)
					continue
				}
				cur.text = text
				blocks = append(blocks, cur)
			case "tc":
				if tableDepth == 1 {
					row = append(row, strings.Join(strings.Fields(cell.String()), " "))
				}
			case "tr":
				if tableDepth == 1 && len(row) > 0 {
					rows = append(rows, row)
				}
			case "tbl":
				if tableDepth == 1 && len(rows) > 0 {
					blocks = append(blocks, docxBlock{rows: rows})
				}
				if tableDepth > 0 {
					tableDepth--
				}
			}
		}
	}
	return blocks, nil
}

// renderDOCX turns blocks into Markdown and plain text.
func renderDOCX(blocks []docxBlock) (string, string) {
	var md, text []string
	for i, b := range blocks {
		switch {
		case b.rows != nil:
			md = append(md, markdownTable(b.rows))
			lines := make([]string, len(b.rows))
			for j, r := range b.rows {
				lines[j] = strings.Join(r, "\t")
			}
			text = append(text, strings.Join(lines, "\n"))
		case b.heading > 0:
			md = append(md, strings.Repeat("#", b.heading)+" "+strings.Join(strings.Fields(b.text), " "))
			text = append(text, b.text)
		case b.list:
			item := "- " + strings.ReplaceAll(b.text, "\n", " ")
			// Consecutive list items form one tight list.
			if i > 0 && blocks[i-1].list && blocks[i-1].rows == nil && blocks[i-1].heading == 0 && len(md) > 0 {
				md[len(md)-1] += "\n" + item
				text[len(text)-1] += "\n" + b.text
				continue
			}
			md = append(md, item)
			text = append(text, b.text)
		default:
			md = append(md, strings.ReplaceAll(b.text, "\t", " "))
			text = append(text, b.text)
		}
	}
	return strings.Join(md, "\n\n"), cleanText(strings.Join(text, "\n\n"))
}

func markdownTable(rows [][]string) string {
	cols := 0
	for _, r := range rows {
		cols = max(cols, len(r))
	}
	var b strings.Builder
	line := func(cells []string) {
		b.WriteString("|")
		for c := 0; c < cols; c++ {
			v := ""
			if c < len(cells) {
				v = strings.ReplaceAll(cells[c], "|", `\|`)
			}
			b.WriteString(" " + v + " |")
		}
		b.WriteString("\n")
	}
	line(rows[0])
	b.WriteString("|" + strings.Repeat(" --- |", cols) + "\n")
	for _, r := range rows[1:] {
		line(r)
	}
	return strings.TrimRight(b.String(), "\n")
}

// applyCoreProperties reads title, author and dates from docProps/core.xml.
func applyCoreProperties(doc *Document, raw []byte) {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	field := ""
	for {
		tok, err := dec.Token()
		if err != nil {
			return
		}
		switch t := tok.(type) {
		case xml.StartElement:
			field = t.Name.Local
		case xml.EndElement:
			field = ""
		case xml.CharData:
			v := strings.TrimSpace(string(t))
			if v == "" {
				continue
			}
			switch field {
			case "title":
				doc.Title = v
			case "creator":
				doc.Metadata["author"] = v
			case "subject", "description":
				if _, ok := doc.Metadata["description"]; !ok {
					doc.Metadata["description"] = v
				}
			case "keywords":
				doc.Metadata["keywords"] = v
			case "created":
				doc.Metadata["published_time"] = v
			case "modified":
				doc.Metadata["modified_time"] = v
			}
		}
	}
}
