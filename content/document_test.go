package content

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestDocumentKind(t *testing.T) {
	pdf := []byte("%PDF-1.4\n...")
	zipBody := []byte("PK\x03\x04rest")
	cases := []struct {
		ct, url string
		body    []byte
		want    string
	}{
		{"application/pdf", "https://x.ir/a", pdf, DocumentPDF},
		{"application/octet-stream", "https://x.ir/download?id=3", pdf, DocumentPDF},
		{"text/html", "https://x.ir/a.pdf", []byte("<html>"), ""},
		{docxContentType, "https://x.ir/a", zipBody, DocumentDOCX},
		{"application/octet-stream", "https://x.ir/files/report.DOCX", zipBody, DocumentDOCX},
		{"application/zip", "https://x.ir/files/archive.zip", zipBody, ""},
		{"application/pdf", "https://x.ir/a.pdf", nil, ""},
	}
	for _, c := range cases {
		if got := DocumentKind(c.ct, c.url, c.body); got != c.want {
			t.Errorf("DocumentKind(%q, %q) = %q, want %q", c.ct, c.url, got, c.want)
		}
	}
	if !IsDocumentURL("https://x.ir/files/a.PDF?x=1") || IsDocumentURL("https://x.ir/pdf") {
		t.Error("IsDocumentURL")
	}
}

// docxFile builds a minimal Word document around a body of WordprocessingML.
func docxFile(t *testing.T, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, content string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	add("_rels/.rels", `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`)
	add("word/document.xml", `<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`+body+`</w:body></w:document>`)
	add("docProps/core.xml", `<?xml version="1.0" encoding="UTF-8"?>
<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/">
<dc:title>گزارش سالانه</dc:title><dc:creator>Mole</dc:creator><dcterms:created>2026-03-01T10:00:00Z</dcterms:created>
</cp:coreProperties>`)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestConvertDOCX(t *testing.T) {
	body := `
<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>قیمت‌ها</w:t></w:r></w:p>
<w:p><w:pPr><w:tabs><w:tab w:val="left" w:pos="720"/></w:tabs></w:pPr><w:r><w:t xml:space="preserve">First </w:t></w:r><w:r><w:t>paragraph</w:t></w:r><w:r><w:delText>deleted</w:delText></w:r></w:p>
<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>one</w:t></w:r></w:p>
<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>two</w:t></w:r></w:p>
<w:tbl>
 <w:tr><w:tc><w:p><w:r><w:t>Model</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>Price</w:t></w:r></w:p></w:tc></w:tr>
 <w:tr><w:tc><w:p><w:r><w:t>A|B</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>۱۲۰٬۰۰۰</w:t></w:r></w:p></w:tc></w:tr>
</w:tbl>
<w:p><w:pPr><w:outlineLvl w:val="1"/></w:pPr><w:r><w:t>Outline</w:t></w:r></w:p>`
	doc, err := ConvertDocument(context.Background(), DocumentDOCX, docxFile(t, body), DocumentOptions{})
	if err != nil {
		t.Fatal(err)
	}
	wantMD := "# قیمت‌ها\n\nFirst paragraph\n\n- one\n- two\n\n| Model | Price |\n| --- | --- |\n| A\\|B | ۱۲۰٬۰۰۰ |\n\n## Outline"
	if doc.Markdown != wantMD {
		t.Errorf("markdown:\n%s\nwant:\n%s", doc.Markdown, wantMD)
	}
	if strings.Contains(doc.Text, "deleted") || !strings.Contains(doc.Text, "Model Price\nA|B") {
		t.Errorf("text = %q", doc.Text)
	}
	if doc.Title != "گزارش سالانه" || doc.Metadata["author"] != "Mole" || doc.Metadata["published_at"] != "2026-03-01T10:00:00Z" {
		t.Errorf("title %q metadata %v", doc.Title, doc.Metadata)
	}
	if doc.Pages != 1 || doc.Metadata["document_type"] != DocumentDOCX || doc.ContentHash == "" {
		t.Errorf("doc = %+v", doc)
	}
	if _, err := ConvertDocument(context.Background(), DocumentDOCX, []byte("PK\x03\x04broken"), DocumentOptions{}); err == nil {
		t.Error("broken docx accepted")
	}
}

// minimalPDF builds a valid one-page-per-text PDF with Helvetica text.
func minimalPDF(pages ...string) []byte {
	var objs []string
	kids := make([]string, len(pages))
	for i := range pages {
		kids[i] = fmt.Sprintf("%d 0 R", 4+2*i)
	}
	objs = append(objs,
		"<< /Type /Catalog /Pages 2 0 R >>",
		fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pages)),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	)
	for i, p := range pages {
		stream := fmt.Sprintf("BT /F1 18 Tf 72 720 Td (%s) Tj ET", p)
		objs = append(objs,
			fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", 5+2*i),
			fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
		)
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return b.Bytes()
}

func TestConvertPDF(t *testing.T) {
	if PDFSupport() == "" {
		t.Skip("pdftotext is not installed")
	}
	pdf := minimalPDF("Hello Mole", "Second page", "Third page")
	doc, err := ConvertDocument(context.Background(), DocumentPDF, pdf, DocumentOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if doc.Pages != 3 || doc.Markdown != "Hello Mole\n\nSecond page\n\nThird page" || doc.Title != "Hello Mole" {
		t.Errorf("doc = %+v", doc)
	}
	if doc.Metadata["total_pages"] != 3 {
		t.Errorf("metadata = %v", doc.Metadata)
	}
	limited, err := ConvertDocument(context.Background(), DocumentPDF, pdf, DocumentOptions{MaxPages: 2})
	if err != nil || limited.Pages != 2 || strings.Contains(limited.Text, "Third") {
		t.Errorf("max pages: %+v %v", limited, err)
	}
	if _, err := ConvertDocument(context.Background(), DocumentPDF, []byte("%PDF-1.4 garbage"), DocumentOptions{}); err == nil {
		t.Error("broken PDF accepted")
	}
}
