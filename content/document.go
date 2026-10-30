package content

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/resolver/crawler/modules"
)

// Document kinds the engine converts to Markdown and text.
const (
	DocumentPDF  = "pdf"
	DocumentDOCX = "docx"
)

// DocumentExtensions are the URL extensions of supported documents.
var DocumentExtensions = []string{".pdf", ".docx"}

const docxContentType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

// DocumentKind reports which supported document a response is ("" for none),
// from its Content-Type, its first bytes and, for generic binary types, the
// URL's extension.
func DocumentKind(contentType, rawURL string, body []byte) string {
	if len(body) == 0 {
		return ""
	}
	ct := strings.ToLower(contentType)
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	ct = strings.TrimSpace(ct)
	switch {
	case bytes.HasPrefix(body, []byte("%PDF-")):
		return DocumentPDF
	case ct == "application/pdf" || ct == "application/x-pdf":
		return DocumentPDF
	case ct == docxContentType:
		return DocumentDOCX
	}
	isZip := bytes.HasPrefix(body, []byte("PK\x03\x04"))
	if isZip && (ct == "" || ct == "application/octet-stream" || ct == "application/zip" || ct == "binary/octet-stream") &&
		urlExtension(rawURL) == ".docx" {
		return DocumentDOCX
	}
	return ""
}

// IsDocumentURL reports whether a URL's path ends with a supported document extension.
func IsDocumentURL(rawURL string) bool {
	ext := urlExtension(rawURL)
	for _, e := range DocumentExtensions {
		if ext == e {
			return true
		}
	}
	return false
}

func urlExtension(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(path.Ext(u.Path))
}

// DocumentOptions bound the work spent on one document.
type DocumentOptions struct {
	MaxPages int           // PDF pages to convert (0 = 500)
	Timeout  time.Duration // whole conversion (0 = 60s)
}

// Document is a converted PDF or DOCX file.
type Document struct {
	Kind        string
	Title       string
	Markdown    string
	Text        string
	Pages       int // pages converted (PDF); 1 for DOCX
	Metadata    map[string]interface{}
	ContentHash string
}

// ErrPDFUnsupported means the poppler tools (pdftotext, pdfinfo) are not installed.
var ErrPDFUnsupported = errors.New("PDF conversion needs poppler-utils (pdftotext) on this machine")

var (
	pdfToolsOnce sync.Once
	pdftotext    string
	pdfinfo      string
)

func findPDFTools() {
	pdfToolsOnce.Do(func() {
		pdftotext, _ = exec.LookPath("pdftotext")
		pdfinfo, _ = exec.LookPath("pdfinfo")
	})
}

// PDFSupport returns the path of pdftotext, or "" when PDFs cannot be converted.
func PDFSupport() string {
	findPDFTools()
	return pdftotext
}

// ConvertDocument turns a PDF or DOCX file into Markdown, text and metadata.
func ConvertDocument(ctx context.Context, kind string, body []byte, opts DocumentOptions) (*Document, error) {
	if opts.Timeout <= 0 {
		opts.Timeout = 60 * time.Second
	}
	if opts.MaxPages <= 0 {
		opts.MaxPages = 500
	}
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	var doc *Document
	var err error
	switch kind {
	case DocumentPDF:
		doc, err = convertPDF(ctx, body, opts.MaxPages)
	case DocumentDOCX:
		doc, err = convertDOCX(body)
	default:
		return nil, fmt.Errorf("unsupported document type %q", kind)
	}
	if err != nil {
		return nil, err
	}
	doc.Kind = kind
	doc.ContentHash = HashText(doc.Text)
	if doc.Metadata == nil {
		doc.Metadata = map[string]interface{}{}
	}
	doc.Metadata["document_type"] = kind
	doc.Metadata["pages"] = doc.Pages
	doc.Metadata["word_count"] = len(strings.Fields(doc.Text))
	if doc.Title == "" {
		doc.Title = firstLine(doc.Text, 200)
	}
	if doc.Title != "" {
		doc.Metadata["title"] = doc.Title
	}
	if v, ok := doc.Metadata["published_time"].(string); ok {
		if t, ok := modules.ParseDateAny(v); ok {
			doc.Metadata["published_at"] = t.UTC().Format("2006-01-02T15:04:05Z")
		}
	}
	return doc, nil
}

// convertPDF runs pdftotext (text, one form feed after each page) and pdfinfo
// (title, author, dates, page count) on a temporary copy of the file.
func convertPDF(ctx context.Context, body []byte, maxPages int) (*Document, error) {
	findPDFTools()
	if pdftotext == "" {
		return nil, ErrPDFUnsupported
	}
	f, err := os.CreateTemp("", "mole-*.pdf")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(body); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}

	out, err := runTool(ctx, pdftotext, "-q", "-enc", "UTF-8", "-l", strconv.Itoa(maxPages), f.Name(), "-")
	if err != nil {
		return nil, fmt.Errorf("pdftotext: %w", err)
	}
	if !utf8.Valid(out) {
		out = bytes.ToValidUTF8(out, []byte("\uFFFD"))
	}
	doc := &Document{Metadata: map[string]interface{}{}}
	pages := strings.Split(bidiControls.Replace(strings.ReplaceAll(string(out), "\r", "")), "\f")
	// pdftotext ends every page with a form feed, so the last element is empty.
	if n := len(pages); n > 0 && strings.TrimSpace(pages[n-1]) == "" {
		pages = pages[:n-1]
	}
	doc.Pages = len(pages)

	var text, md []string
	for _, p := range pages {
		if t := cleanText(p); t != "" {
			text = append(text, t)
			md = append(md, pdfPageMarkdown(t))
		}
	}
	doc.Text = strings.Join(text, "\n\n")
	doc.Markdown = strings.Join(md, "\n\n")

	if pdfinfo != "" {
		if info, err := runTool(ctx, pdfinfo, "-isodates", "-enc", "UTF-8", f.Name()); err == nil {
			applyPDFInfo(doc, string(info))
		}
	}
	return doc, nil
}

// bidiControls removes the embedding and isolate marks pdftotext puts around
// right-to-left runs (Persian text is already in logical order). The
// zero-width non-joiner, which Persian words need, is kept.
var bidiControls = strings.NewReplacer(
	"\u202a", "", "\u202b", "", "\u202c", "", "\u202d", "", "\u202e", "",
	"\u2066", "", "\u2067", "", "\u2068", "", "\u2069", "",
)

// pdfPageMarkdown joins the hard-wrapped lines of each paragraph of a page.
func pdfPageMarkdown(page string) string {
	paras := strings.Split(page, "\n\n")
	for i, p := range paras {
		paras[i] = strings.Join(strings.Fields(p), " ")
	}
	return strings.Join(paras, "\n\n")
}

func applyPDFInfo(doc *Document, info string) {
	for _, line := range strings.Split(info, "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		if val == "" {
			continue
		}
		switch strings.TrimSpace(key) {
		case "Title":
			doc.Title = val
		case "Author":
			doc.Metadata["author"] = val
		case "Subject":
			doc.Metadata["description"] = val
		case "Keywords":
			doc.Metadata["keywords"] = val
		case "CreationDate":
			doc.Metadata["published_time"] = val
		case "ModDate":
			doc.Metadata["modified_time"] = val
		case "Pages":
			if n, err := strconv.Atoi(val); err == nil {
				doc.Metadata["total_pages"] = n
			}
		}
	}
}

func runTool(ctx context.Context, name string, args ...string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("timed out")
		}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%s", firstLine(msg, 300))
		}
		return nil, err
	}
	return stdout.Bytes(), nil
}

func firstLine(s string, max int) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			if utf8.RuneCountInString(l) > max {
				l = string([]rune(l)[:max])
			}
			return l
		}
	}
	return ""
}
