package catalog

import (
	"testing"

	"github.com/getbooky/booky/internal/metadata"
)

// Editing the author moves the book to that author's page, find-or-creating
// the author, and locks the field so a refresh can't drag it back.
func TestEditBookMovesAuthor(t *testing.T) {
	s := testStore(t)
	id, err := s.UpsertBook(metadata.BookMeta{Provider: "t", Title: "Breaking the Pattern", Authors: []string{"Livia Sparow"}})
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.GetBook(id)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.EditBook(id, map[string]string{"author": "Livia Sparrow"}, true); err != nil {
		t.Fatalf("EditBook: %v", err)
	}
	after, err := s.GetBook(id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Author != "Livia Sparrow" {
		t.Errorf("author = %q, want %q", after.Author, "Livia Sparrow")
	}
	if after.AuthorID == before.AuthorID {
		t.Error("the book still points at the old author row")
	}
	if !after.FieldLocks["author"] {
		t.Error("an edited author must lock against refreshes")
	}
	// the book is on the new author's page, not the old one
	byNew, err := s.ListBooks(after.AuthorID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(byNew) != 1 || byNew[0].ID != id {
		t.Errorf("new author's page = %+v", byNew)
	}
	byOld, err := s.ListBooks(before.AuthorID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(byOld) != 0 {
		t.Errorf("old author still lists the book: %+v", byOld)
	}
}

// An existing author is reused rather than duplicated, and the book's series
// moves with it — series rows belong to an author, so a left-behind series
// would file the book under someone else's page.
func TestEditBookAuthorReusesRowAndMovesSeries(t *testing.T) {
	s := testStore(t)
	keeper, err := s.UpsertBook(metadata.BookMeta{Provider: "t", Title: "Things We Almost Kept", Authors: []string{"Livia Sparrow"}})
	if err != nil {
		t.Fatal(err)
	}
	target, err := s.GetBook(keeper)
	if err != nil {
		t.Fatal(err)
	}

	id, err := s.UpsertBook(metadata.BookMeta{
		Provider: "t", Title: "Breaking the Pattern", Authors: []string{"Somebody Else"},
		SeriesName: "Piper Hollow", SeriesIndex: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EditBook(id, map[string]string{"author": "livia sparrow"}, true); err != nil {
		t.Fatalf("EditBook: %v", err)
	}
	moved, err := s.GetBook(id)
	if err != nil {
		t.Fatal(err)
	}
	if moved.AuthorID != target.AuthorID {
		t.Errorf("author id = %d, want the existing %d (name match is case-insensitive)", moved.AuthorID, target.AuthorID)
	}
	if moved.SeriesName != "Piper Hollow" {
		t.Errorf("series lost in the move: %+v", moved)
	}
	series, err := s.ListSeries(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, sr := range series {
		if sr.Name == "Piper Hollow" && sr.AuthorID != target.AuthorID {
			t.Errorf("series %q still belongs to author %d, want %d", sr.Name, sr.AuthorID, target.AuthorID)
		}
	}
}

func TestEditBookRejectsEmptyAuthor(t *testing.T) {
	s := testStore(t)
	id, err := s.UpsertBook(metadata.BookMeta{Provider: "t", Title: "Burrow", Authors: []string{"Emmett Hale"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EditBook(id, map[string]string{"author": "   "}, true); err == nil {
		t.Error("an empty author should be rejected, not silently applied")
	}
	b, err := s.GetBook(id)
	if err != nil {
		t.Fatal(err)
	}
	if b.Author != "Emmett Hale" {
		t.Errorf("author changed to %q despite the rejection", b.Author)
	}
}

// A refresh after an author edit must not drag the book back: identity
// matching finds the row, and the author column is never rewritten by upsert.
func TestAuthorEditSurvivesRefresh(t *testing.T) {
	s := testStore(t)
	meta := metadata.BookMeta{Provider: "t", Title: "Breaking the Pattern", Authors: []string{"Livia Frost"}, HardcoverID: "hc-1"}
	id, err := s.UpsertBook(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EditBook(id, map[string]string{"author": "Livia Sparrow"}, true); err != nil {
		t.Fatal(err)
	}
	again, err := s.UpsertBook(meta) // the provider still credits the old author
	if err != nil {
		t.Fatal(err)
	}
	if again != id {
		t.Fatalf("refresh minted a duplicate row %d (original %d)", again, id)
	}
	b, err := s.GetBook(id)
	if err != nil {
		t.Fatal(err)
	}
	if b.Author != "Livia Sparrow" {
		t.Errorf("author = %q, want the hand-edited %q to survive", b.Author, "Livia Sparrow")
	}
}
