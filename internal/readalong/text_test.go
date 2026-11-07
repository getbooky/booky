package readalong

import (
	"bytes"
	"encoding/xml"
	"io"
	"strings"
	"testing"
)

func wellFormed(t *testing.T, doc []byte) {
	t.Helper()
	d := xml.NewDecoder(bytes.NewReader(doc))
	d.Strict = true
	d.Entity = xml.HTMLEntity
	for {
		_, err := d.Token()
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatalf("output is not well-formed XML: %v\n%s", err, doc)
		}
	}
}

func texts(units []Unit) []string {
	out := make([]string, len(units))
	for i, u := range units {
		out[i] = u.Text
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

const page = `<?xml version="1.0" encoding="utf-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops">
<head><title>Chapter I. Down the Rabbit-Hole</title>
<style>p > em { font-style: italic }</style></head>
<body>
<h2 class="chapter" title="a > b">Chapter I.</h2>
<p>Alice was beginning to get very tired. Once or twice she had peeped into the book!</p>
<p>“What is the use of a book,” thought Alice, “without pictures?” She considered.</p>
<p>Mr. Darcy met J. R. R. Tolkien at St. Paul’s. Tom &amp; Jerry left&#8230; Later.</p>
<p>A <em>very</em> odd thing.</p>
<!-- a comment. With sentences. -->
<p><![CDATA[raw. text.]]></p>
<p>* * *</p>
<script>var a = "no. wrap.";</script>
<p>   </p>
<img src="x.png" alt="a picture."/>
</body>
</html>`

func TestWrapSentences(t *testing.T) {
	out, units, next, err := WrapSentences([]byte(page), "text/ch1.xhtml", 1)
	if err != nil {
		t.Fatal(err)
	}
	wellFormed(t, out)

	want := []string{
		"Chapter I.",
		"Alice was beginning to get very tired.",
		"Once or twice she had peeped into the book!",
		"“What is the use of a book,” thought Alice, “without pictures?”",
		"She considered.",
		"Mr. Darcy met J. R. R. Tolkien at St. Paul’s.",
		"Tom & Jerry left…",
		"Later.",
		"A",
		"very",
		"odd thing.",
	}
	if got := texts(units); !eq(got, want) {
		t.Fatalf("units:\n%q\nwant:\n%q", got, want)
	}
	if next != 1+len(want) {
		t.Fatalf("next = %d, want %d", next, 1+len(want))
	}
	for i, u := range units {
		if u.Href != "text/ch1.xhtml" {
			t.Fatalf("unit %d href = %q", i, u.Href)
		}
		if !bytes.Contains(out, []byte(`<span id="`+u.ID+`">`)) {
			t.Fatalf("no span for %s", u.ID)
		}
	}
	s := string(out)
	// entities pass through as written
	if !strings.Contains(s, `<span id="booky-s7">Tom &amp; Jerry left&#8230;</span>`) {
		t.Fatalf("entities changed:\n%s", s)
	}
	// nothing outside the body text is touched
	for _, keep := range []string{
		`<title>Chapter I. Down the Rabbit-Hole</title>`,
		`<style>p > em { font-style: italic }</style>`,
		`<h2 class="chapter" title="a > b">`,
		`<!-- a comment. With sentences. -->`,
		`<![CDATA[raw. text.]]>`,
		`<p>* * *</p>`,
		`<script>var a = "no. wrap.";</script>`,
		`<p>   </p>`,
		`<img src="x.png" alt="a picture."/>`,
		`<?xml version="1.0" encoding="utf-8"?>`,
	} {
		if !strings.Contains(s, keep) {
			t.Errorf("%q was altered", keep)
		}
	}
	// whitespace stays outside spans, so the layout doesn't change
	if !strings.Contains(s, `tired.</span> <span id="booky-s3">Once`) {
		t.Errorf("whitespace between sentences moved:\n%s", s)
	}
}

func TestWrapSentencesNumbersAcrossDocuments(t *testing.T) {
	doc := `<html><body><p>One. Two.</p></body></html>`
	_, u1, next, err := WrapSentences([]byte(doc), "a.xhtml", 1)
	if err != nil {
		t.Fatal(err)
	}
	_, u2, next, err := WrapSentences([]byte(doc), "b.xhtml", next)
	if err != nil {
		t.Fatal(err)
	}
	if u1[0].ID != "booky-s1" || u2[0].ID != "booky-s3" || next != 5 {
		t.Fatalf("ids %s %s next %d", u1[0].ID, u2[0].ID, next)
	}
}

func TestWrapSentencesRejectsBrokenMarkup(t *testing.T) {
	for _, doc := range []string{
		`<html><body><p class="x>text</p></body></html>`,
		`<html><body><!-- never closed </body></html>`,
	} {
		if _, _, _, err := WrapSentences([]byte(doc), "x.xhtml", 1); err == nil {
			t.Errorf("no error for %q", doc)
		}
	}
}

func TestWrapSentencesPrefixedElements(t *testing.T) {
	doc := `<h:html xmlns:h="http://www.w3.org/1999/xhtml"><h:head><h:title>Skip. Me.</h:title></h:head><h:body><h:p>Read. This.</h:p></h:body></h:html>`
	out, units, _, err := WrapSentences([]byte(doc), "x.xhtml", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := texts(units); !eq(got, []string{"Read.", "This."}) {
		t.Fatalf("units = %q", got)
	}
	if !strings.Contains(string(out), "<h:title>Skip. Me.</h:title>") {
		t.Fatal("title was wrapped")
	}
}

func TestSplitSentences(t *testing.T) {
	cases := map[string][]string{
		"Hello there. General Kenobi!":     {"Hello there.", " General Kenobi!"},
		"Wait... what?! Really.":           {"Wait...", " what?!", " Really."},
		`He said "Stop." Then left.`:       {`He said "Stop."`, " Then left."},
		"The end.":                         {"The end."},
		"No terminal":                      {"No terminal"},
		"Dr. Who and Mrs. Hudson arrived.": {"Dr. Who and Mrs. Hudson arrived."},
		"3.14 is pi. Yes.":                 {"3.14 is pi.", " Yes."},
		"Ünïcödé. Ωmega.":                  {"Ünïcödé.", " Ωmega."},
	}
	for in, want := range cases {
		var got []string
		for _, p := range splitSentences([]byte(in)) {
			got = append(got, string(p))
		}
		if !eq(got, want) {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}
