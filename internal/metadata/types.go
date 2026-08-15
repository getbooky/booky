// Package metadata resolves books across providers (Goodreads, Hardcover,
// Open Library) and merges their answers by user-configured priority.
// Identity rule: a book converges on Goodreads ID + ISBN-13 + Hardcover ID.
package metadata

import "context"

type SearchParams struct {
	Query  string // free-text; used when Title/Author are not split
	Title  string
	Author string
	ISBN   string
	Limit  int
}

// BookMeta is one provider's view of a book. Zero values mean "unknown" and
// are filled by lower-priority providers during merge.
type BookMeta struct {
	Provider     string   `json:"provider"`
	Title        string   `json:"title"`
	Subtitle     string   `json:"subtitle,omitempty"`
	Authors      []string `json:"authors,omitempty"`
	Description  string   `json:"description,omitempty"`
	Publisher    string   `json:"publisher,omitempty"`
	Language     string   `json:"language,omitempty"`
	SeriesName   string   `json:"seriesName,omitempty"`
	SeriesIndex  float64  `json:"seriesIndex,omitempty"`
	ISBN10       string   `json:"isbn10,omitempty"`
	ISBN13       string   `json:"isbn13,omitempty"`
	GoodreadsID  string   `json:"goodreadsId,omitempty"`
	HardcoverID  string   `json:"hardcoverId,omitempty"`
	CoverURL     string   `json:"coverUrl,omitempty"`
	ReleaseDate  string   `json:"releaseDate,omitempty"` // YYYY-MM-DD or YYYY
	RatingsCount int      `json:"ratingsCount,omitempty"`
	Genres       []string `json:"genres,omitempty"`
	// Compilation marks box sets / omnibus / collections — never shown as books.
	Compilation bool `json:"compilation,omitempty"`
}

type Provider interface {
	Key() string
	Search(ctx context.Context, p SearchParams) ([]BookMeta, error)
}

// Merge fills dst's zero-valued fields from src (dst wins on conflicts).
func Merge(dst *BookMeta, src BookMeta) {
	if dst.Title == "" {
		dst.Title = src.Title
	}
	if dst.Subtitle == "" {
		dst.Subtitle = src.Subtitle
	}
	if len(dst.Authors) == 0 {
		dst.Authors = src.Authors
	}
	if dst.Description == "" {
		dst.Description = src.Description
	}
	if dst.Publisher == "" {
		dst.Publisher = src.Publisher
	}
	if dst.Language == "" {
		dst.Language = src.Language
	}
	if dst.SeriesName == "" {
		dst.SeriesName = src.SeriesName
		dst.SeriesIndex = src.SeriesIndex
	}
	if dst.ISBN10 == "" {
		dst.ISBN10 = src.ISBN10
	}
	if dst.ISBN13 == "" {
		dst.ISBN13 = src.ISBN13
	}
	if dst.GoodreadsID == "" {
		dst.GoodreadsID = src.GoodreadsID
	}
	if dst.HardcoverID == "" {
		dst.HardcoverID = src.HardcoverID
	}
	if dst.CoverURL == "" {
		dst.CoverURL = src.CoverURL
	}
	if dst.ReleaseDate == "" {
		dst.ReleaseDate = src.ReleaseDate
	}
	// Popularity scales differ per provider (Goodreads ratings dwarf
	// Hardcover users); keep the largest signal rather than the first.
	if src.RatingsCount > dst.RatingsCount {
		dst.RatingsCount = src.RatingsCount
	}
	if len(dst.Genres) == 0 {
		dst.Genres = src.Genres
	}
}
