package release

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

var profile = Profile{
	Formats:        []string{"epub", "azw3", "mobi"},
	PreferredTerms: []string{"retail"},
	AvoidedTerms:   []string{"scan", "ocr"},
}

func TestDetectFormat(t *testing.T) {
	cases := map[string]string{
		"First Ember (2023) Retail EPUB":        "epub",
		"Mara Voss - First Ember [AZW3]":        "azw3",
		"First.Ember.2023.eBook.PDF-GROUP":      "pdf",
		"First Ember by Mara Voss":              "",
		"The Market of Masks (epub+mobi packs)": "epub",
	}
	for title, want := range cases {
		if got := DetectFormat(title); got != want {
			t.Errorf("DetectFormat(%q) = %q, want %q", title, got, want)
		}
	}
}

func TestFormatOrderDominates(t *testing.T) {
	// a "scan" epub must still beat a pristine retail mobi: format first
	releases := []Release{
		{Title: "First Ember retail MOBI", Source: "prowlarr:ndx"},
		{Title: "First Ember scan OCR EPUB", Source: "prowlarr:ndx"},
	}
	ranked := Rank(releases, profile, []string{"prowlarr"})
	if ranked[0].Format != "epub" {
		t.Fatalf("expected epub first despite avoided terms, got %+v", ranked[0])
	}
}

func TestTermsBreakFormatTies(t *testing.T) {
	releases := []Release{
		{Title: "First Ember EPUB (scan)", Source: "annas"},
		{Title: "First Ember Retail EPUB", Source: "annas"},
	}
	ranked := Rank(releases, profile, nil)
	if ranked[0].Title != "First Ember Retail EPUB" {
		t.Fatalf("preferred term should win the tie: %+v", ranked)
	}
}

func TestSourceOrderBreaksRemainingTies(t *testing.T) {
	releases := []Release{
		{Title: "First Ember EPUB", Source: "libgen"},
		{Title: "First Ember EPUB", Source: "prowlarr:nzbgeek"},
	}
	ranked := Rank(releases, profile, []string{"prowlarr", "annas", "libgen"})
	if ranked[0].Source != "prowlarr:nzbgeek" {
		t.Fatalf("source priority should break the tie: %+v", ranked)
	}
}

func TestUnlistedFormatsFilteredUnlessOnlyOption(t *testing.T) {
	releases := []Release{
		{Title: "First Ember EPUB", Source: "annas"},
		{Title: "First Ember PDF", Source: "annas"},
	}
	ranked := Rank(releases, profile, nil)
	if len(ranked) != 1 || ranked[0].Format != "epub" {
		t.Fatalf("pdf should be filtered when epub exists: %+v", ranked)
	}

	only := []Release{{Title: "First Ember PDF", Source: "annas"}}
	ranked = Rank(only, profile, nil)
	if len(ranked) != 1 {
		t.Fatalf("wrong-format release should survive when it's the only option: %+v", ranked)
	}
}

func TestRankIsStableForEqualScores(t *testing.T) {
	releases := []Release{
		{Title: "First Ember EPUB", Source: "annas", DownloadURL: "a"},
		{Title: "First Ember EPUB", Source: "annas", DownloadURL: "b"},
	}
	ranked := Rank(releases, profile, nil)
	if ranked[0].DownloadURL != "a" {
		t.Fatalf("equal scores should keep input order: %+v", ranked)
	}
}

func TestDownloadFilename(t *testing.T) {
	mk := func(cd string) *http.Response {
		h := http.Header{}
		if cd != "" {
			h.Set("Content-Disposition", cd)
		}
		return &http.Response{Header: h}
	}
	cases := []struct {
		name       string
		res        *http.Response
		url        string
		wantFormat string
		fallback   string
		want       string
	}{
		{"content-disposition wins", mk(`attachment; filename="The Silent Comet.epub"`), "https://x/redirection", "epub", "dl", "The Silent Comet.epub"},
		{"redirection url + wantFormat backfills ext", mk(""), "https://x/redirection", "pdf", "dl", "redirection.pdf"},
		{"url base kept when it has an ext", mk(""), "https://x/dl/book.mobi?token=1", "epub", "dl", "book.mobi"},
		{"empty everything falls back", mk(""), "", "", "annas-download", "annas-download"},
		{"nil response, url base", nil, "https://x/dl/a.azw3", "", "dl", "a.azw3"},
		{"cd path components stripped", mk(`attachment; filename="/etc/passwd"`), "https://x/redirection", "", "dl", "passwd"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DownloadFilename(c.res, c.url, c.wantFormat, c.fallback); got != c.want {
				t.Errorf("DownloadFilename = %q, want %q", got, c.want)
			}
		})
	}
}

func TestDetectLanguage(t *testing.T) {
	cases := map[string]string{
		"Iron Ash - Cole Merrick SWEDISH Retail EPUB": "swedish",
		"Iron Ash (Svenska) epub":                     "swedish",
		"Cole.Merrick.Iron.Ash.GERMAN.eBook-GROUP":    "german",
		"Iron Ash [sv] epub":                          "swedish",
		"Iron Ash (GER) epub":                         "german",
		"Iron Ash - Cole Merrick (2018) Retail EPUB":  "",
		// bare two-letter words must never read as languages
		"No Refuge for Old Ghosts epub": "",
		"It Ends at Dawn - CarysVane":   "",
	}
	for title, want := range cases {
		if got := DetectLanguage(title); got != want {
			t.Errorf("DetectLanguage(%q) = %q, want %q", title, got, want)
		}
	}
}

func TestNormalizeLanguage(t *testing.T) {
	cases := map[string]string{
		"sv": "swedish", "Swedish": "swedish", "svenska": "swedish",
		"English [en]": "english", "eng": "english",
		"": "", "klingon": "klingon",
	}
	for in, want := range cases {
		if got := NormalizeLanguage(in); got != want {
			t.Errorf("NormalizeLanguage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWrongLanguageNeverAutoGrabbable(t *testing.T) {
	p := profile
	p.Languages = []string{"english"}
	releases := []Release{
		// best format, but tagged Swedish two different ways
		{Title: "Iron Ash SWEDISH Retail EPUB", Source: "prowlarr:ndx"},
		{Title: "Iron Ash epub", Source: "annas", Language: "sv"},
		// worse format, no language claim: must win
		{Title: "Iron Ash retail MOBI", Source: "prowlarr:ndx"},
	}
	ranked := Rank(releases, p, []string{"prowlarr", "annas"})
	if ranked[0].Format != "mobi" {
		t.Fatalf("expected untagged mobi to outrank swedish epubs, got %+v", ranked[0])
	}
	// the swedish copies are dropped entirely while alternatives exist
	for _, r := range ranked {
		if DetectLanguage(r.Title) == "swedish" || NormalizeLanguage(r.Language) == "swedish" {
			t.Fatalf("swedish release survived ranking: %+v", r)
		}
	}
}

func TestWrongLanguageKeptOnlyAsLastResortAndNegative(t *testing.T) {
	p := profile
	p.Languages = []string{"english"}
	releases := []Release{{Title: "Iron Ash SWEDISH EPUB", Source: "annas"}}
	ranked := Rank(releases, p, []string{"annas"})
	if len(ranked) != 1 {
		t.Fatalf("last-resort release should stay visible for manual picks, got %d", len(ranked))
	}
	if ranked[0].Score > 0 {
		t.Fatalf("wrong-language release must score non-positive (AutoGrab gate), got %d", ranked[0].Score)
	}
}

func TestLanguageWordInBookTitleNotPenalized(t *testing.T) {
	p := profile
	p.Languages = []string{"english"}
	p.BookTitle = "French Knots"
	releases := []Release{{Title: "French Knots - Ada Merrow (2022) EPUB", Source: "annas"}}
	ranked := Rank(releases, p, []string{"annas"})
	if ranked[0].Score <= 0 {
		t.Fatalf("book's own title tripped the language filter: %+v", ranked[0])
	}
	// but explicit metadata still rejects even when the title matches the book
	releases[0].Language = "fr"
	ranked = Rank(releases, p, []string{"annas"})
	if ranked[0].Score > 0 {
		t.Fatalf("explicit fr metadata should be penalized regardless of title, got %+v", ranked[0])
	}
}

func TestEmptyLanguageListDisablesFilter(t *testing.T) {
	releases := []Release{{Title: "Iron Ash SWEDISH Retail EPUB", Source: "annas"}}
	ranked := Rank(releases, profile, []string{"annas"})
	if ranked[0].Score <= 0 {
		t.Fatalf("no accepted-language list means no filtering, got %+v", ranked[0])
	}
}

func TestDownloadFilenameTruncatesOverlongNames(t *testing.T) {
	long := strings.Repeat("Very Long Subtitle ", 20) // ~380 bytes
	got := DownloadFilename(nil, "https://x/"+long+".epub", "epub", "fb")
	if len(got) > 200 {
		t.Fatalf("name still %d bytes: %q", len(got), got)
	}
	if filepath.Ext(got) != ".epub" {
		t.Fatalf("extension lost in truncation: %q", got)
	}
	// multi-byte runes never split at the cut
	long = strings.Repeat("ö", 150) + ".epub"
	got = DownloadFilename(nil, "https://x/"+long, "epub", "fb")
	if !utf8.ValidString(got) {
		t.Fatalf("truncation split a rune: %q", got)
	}
	// short names pass through untouched
	if got := DownloadFilename(nil, "https://x/iron-ash.epub", "epub", "fb"); got != "iron-ash.epub" {
		t.Fatalf("short name mangled: %q", got)
	}
}

func TestMultiLanguageRecordRejectedIfAnyUnwanted(t *testing.T) {
	p := profile
	p.Languages = []string{"english"}
	// a translated edition tagged with original AND edition language
	frRel := Release{Title: "La forme de la fumée", Source: "annas", Language: "english,french"}
	enRel := Release{Title: "The Shape of Smoke EPUB", Source: "annas", Language: "english"}
	ranked := Rank([]Release{frRel, enRel}, p, []string{"annas"})
	if len(ranked) != 1 || ranked[0].Language != "english" {
		t.Fatalf("dual-tagged translation should be dropped, got %+v", ranked)
	}
}
