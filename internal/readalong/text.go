// Package readalong lines an audiobook up with its ebook: every sentence of
// the EPUB gets an id, the narration is transcribed into timestamped words,
// and the two are matched so each sentence knows when it is spoken. The
// result is a read-along edition — the EPUB with EPUB 3 media overlays (SMIL)
// — plus a timing file devices can follow.
package readalong

import (
	"bytes"
	"fmt"
	"html"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Unit is one stretch of narrated text: a sentence, or the part of one that
// sits in a single text node (a sentence running through <em>…</em> becomes
// several units). Units are what get ids, timings and highlights.
type Unit struct {
	ID   string `json:"id"`
	Href string `json:"href"` // the content document, as the OPF manifest names it
	Text string `json:"-"`
}

// idPrefix marks the spans Booky inserts; nothing else in a book uses it.
const idPrefix = "booky-s"

// skipElements hold text that is never narrated (or isn't text at all).
var skipElements = map[string]bool{
	"head": true, "script": true, "style": true, "svg": true, "math": true,
	"rt": true, "rp": true, "title": true,
}

// WrapSentences wraps every sentence of an XHTML document's body text in
// <span id="booky-sN">, numbering from next, and returns the rewritten
// document, the units in reading order, and the next free number.
//
// It works on the raw bytes rather than through an XML parser: only text
// between tags is touched, each span opens and closes inside the same text
// node, and every other byte — markup, entities, namespaces, the XML
// declaration — passes through untouched, so the document stays exactly as
// well-formed as it came in.
func WrapSentences(doc []byte, href string, next int) ([]byte, []Unit, int, error) {
	var out bytes.Buffer
	out.Grow(len(doc) + len(doc)/8)
	var units []Unit
	var stack []string // open element names, lowercased, namespace prefix dropped
	inBody := false
	skipping := func() bool {
		for _, name := range stack {
			if skipElements[name] {
				return true
			}
		}
		return false
	}

	i := 0
	for i < len(doc) {
		if doc[i] != '<' {
			j := bytes.IndexByte(doc[i:], '<')
			if j < 0 {
				j = len(doc) - i
			}
			text := doc[i : i+j]
			if inBody && !skipping() {
				var u []Unit
				next, u = wrapText(&out, text, href, next)
				units = append(units, u...)
			} else {
				out.Write(text)
			}
			i += j
			continue
		}

		end, err := markupEnd(doc, i)
		if err != nil {
			return nil, nil, next, fmt.Errorf("%s: %w", href, err)
		}
		tag := doc[i:end]
		out.Write(tag)
		i = end

		name, closing, selfClosing, isElement := tagInfo(tag)
		if !isElement {
			continue
		}
		switch {
		case closing:
			// pop to the matching element; tolerate stray closers
			for k := len(stack) - 1; k >= 0; k-- {
				if stack[k] == name {
					stack = stack[:k]
					break
				}
			}
			if name == "body" {
				inBody = false
			}
		case !selfClosing:
			stack = append(stack, name)
			if name == "body" {
				inBody = true
			}
		}
	}
	return out.Bytes(), units, next, nil
}

// markupEnd returns the index just past the markup starting at doc[i] ('<'):
// a comment, CDATA section, processing instruction, doctype, or tag. Quoted
// attribute values may contain '>'.
func markupEnd(doc []byte, i int) (int, error) {
	rest := doc[i:]
	find := func(term string) (int, error) {
		k := bytes.Index(rest, []byte(term))
		if k < 0 {
			return 0, fmt.Errorf("unterminated %q at byte %d", string(rest[:min(len(rest), 12)]), i)
		}
		return i + k + len(term), nil
	}
	switch {
	case bytes.HasPrefix(rest, []byte("<!--")):
		return find("-->")
	case bytes.HasPrefix(rest, []byte("<![CDATA[")):
		return find("]]>")
	case bytes.HasPrefix(rest, []byte("<?")):
		return find("?>")
	}
	var quote byte
	for k := 1; k < len(rest); k++ {
		c := rest[k]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '>':
			return i + k + 1, nil
		}
	}
	return 0, fmt.Errorf("unterminated tag at byte %d", i)
}

// tagInfo reads an element tag's local name and shape. Comments, CDATA,
// doctypes and processing instructions report isElement=false.
func tagInfo(tag []byte) (name string, closing, selfClosing, isElement bool) {
	if len(tag) < 3 || tag[1] == '!' || tag[1] == '?' {
		return "", false, false, false
	}
	body := tag[1 : len(tag)-1]
	if body[0] == '/' {
		closing = true
		body = body[1:]
	}
	if len(body) > 0 && body[len(body)-1] == '/' {
		selfClosing = true
	}
	end := bytes.IndexFunc(body, func(r rune) bool { return unicode.IsSpace(r) || r == '/' })
	if end < 0 {
		end = len(body)
	}
	name = strings.ToLower(string(body[:end]))
	if k := strings.LastIndexByte(name, ':'); k >= 0 {
		name = name[k+1:]
	}
	return name, closing, selfClosing, name != ""
}

// wrapText writes one text node with each sentence in it wrapped. Leading
// and trailing whitespace stays outside the spans, and pieces with no
// letters or digits (a lone dash, an ornament) stay unwrapped.
func wrapText(out *bytes.Buffer, text []byte, href string, next int) (int, []Unit) {
	var units []Unit
	for _, piece := range splitSentences(text) {
		lead, core, trail := trimSpace(piece)
		out.Write(lead)
		plain := html.UnescapeString(string(core))
		if len(core) == 0 || !hasWordChar(plain) {
			out.Write(core)
			out.Write(trail)
			continue
		}
		id := fmt.Sprintf("%s%d", idPrefix, next)
		next++
		fmt.Fprintf(out, `<span id="%s">`, id)
		out.Write(core)
		out.WriteString(`</span>`)
		out.Write(trail)
		units = append(units, Unit{ID: id, Href: href, Text: plain})
	}
	return next, units
}

// splitSentences cuts raw (still entity-escaped) text after sentence-ending
// punctuation — . ! ? … — plus any closing quotes or brackets, where
// whitespace follows. A cut never lands inside an entity, since entities end
// in ';'.
func splitSentences(text []byte) [][]byte {
	var pieces [][]byte
	start := 0
	for i := 0; i < len(text); {
		r, size := decodeRune(text[i:])
		i += size
		if !isTerminal(r) || (r == '.' && abbreviationBefore(text[start:i-size])) {
			continue
		}
		// absorb closing punctuation: ." ?) !’ …”
		for i < len(text) {
			c, n := decodeRune(text[i:])
			if !isCloser(c) {
				break
			}
			i += n
		}
		if next, _ := decodeRune(text[i:]); i < len(text) && unicode.IsSpace(next) {
			pieces = append(pieces, text[start:i])
			start = i
		}
	}
	if start < len(text) {
		pieces = append(pieces, text[start:])
	}
	return pieces
}

// decodeRune reads one character of raw text, decoding a character reference
// (&#8230; &hellip; &rdquo;) as the character it stands for, so punctuation
// written as an entity ends sentences like the real thing.
func decodeRune(b []byte) (rune, int) {
	if len(b) > 0 && b[0] == '&' {
		if end := bytes.IndexByte(b[:min(len(b), 12)], ';'); end > 1 {
			decoded := html.UnescapeString(string(b[:end+1]))
			if r, n := utf8.DecodeRuneInString(decoded); n == len(decoded) && decoded != string(b[:end+1]) {
				return r, end + 1
			}
		}
	}
	return utf8.DecodeRune(b)
}

func isTerminal(r rune) bool { return r == '.' || r == '!' || r == '?' || r == '…' }

func isCloser(r rune) bool {
	switch r {
	case '"', '\'', '”', '’', ')', ']', '»', '.', '!', '?', '…':
		return true
	}
	return false
}

// abbreviationBefore keeps "Mr. Darcy" and "J. R. R. Tolkien" in one
// sentence: a period after a title from a short list, or after a single
// capital letter, doesn't end one. before is the text up to the period.
func abbreviationBefore(before []byte) bool {
	k := len(before)
	for k > 0 {
		r, n := utf8.DecodeLastRune(before[:k])
		if !unicode.IsLetter(r) {
			break
		}
		k -= n
	}
	word := string(before[k:])
	switch strings.ToLower(word) {
	case "mr", "mrs", "ms", "dr", "st", "jr", "sr", "prof", "rev", "gen", "col", "capt", "lt", "mt", "vs", "no":
		return true
	}
	r, n := utf8.DecodeRuneInString(word)
	return n > 0 && n == len(word) && unicode.IsUpper(r)
}

func trimSpace(b []byte) (lead, core, trail []byte) {
	l := len(b) - len(bytes.TrimLeftFunc(b, unicode.IsSpace))
	rest := b[l:]
	c := len(bytes.TrimRightFunc(rest, unicode.IsSpace))
	return b[:l], rest[:c], rest[c:]
}

func hasWordChar(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}
