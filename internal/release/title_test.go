package release

import "testing"

func TestTitleMatches(t *testing.T) {
	const maas = "Sarah J. Maas"
	cases := []struct {
		book, author, release string
		want                  bool
	}{
		// the reported search: every result was another book in the series
		{"A Court of Forgotten Melody", maas, "A Court of Silver Flames (A Court of Thorns and Roses #4) — Sarah J. Maas [...] .epub", false},
		{"A Court of Forgotten Melody", maas, "A Court of Thorns and Roses (Court of Thorns & Roses #1) — Sarah J. Maas .epub", false},
		{"A Court of Forgotten Melody", maas, "Court of Thorns and Roses 5: A Court of Splintered Harmony — Sarah J. Maas .epub", false},
		{"A Court of Forgotten Melody", maas, "A Court of Forgotten Melody — Sarah J. Maas .epub", true},

		// the series name must not stand in for book one
		{"A Court of Thorns and Roses", maas, "A Court of Frost and Starlight (A Court of Thorns and Roses) — Sarah J. Maas .epub", false},
		{"A Court of Thorns and Roses", maas, "A Court of Silver Flames (A Court of Thorns and Roses #4) — Sarah J. Maas .epub", false},
		{"A Court of Thorns and Roses", maas, "Court of Thorns and Roses 3: A Court of Wings and Ruin — Sarah J. Maas .epub", false},
		{"A Court of Thorns and Roses", maas, "Court of Thorns and Roses 1: A Court of Thorns and Roses — Sarah J. Maas .epub", true},
		{"A Court of Thorns and Roses", maas, "A Court of Thorns and Roses (Court of Thorns & Roses #1) — Sarah J. Maas .epub", true},
		{"A Court of Wings and Ruin", maas, "Court of Thorns and Roses 3: A Court of Wings and Ruin — Sarah J. Maas .epub", true},

		// usenet and audiobook naming
		{"A Court of Thorns and Roses", maas, "Sarah.J.Maas.A.Court.of.Thorns.and.Roses.2015.RETAIL.EPUB.eBook-GRP", true},
		{"A Court of Thorns and Roses", maas, "Sarah J Maas - ACOTAR 01 - A Court of Thorns and Roses (Unabridged) M4B", true},
		{"A Court of Thorns and Roses", maas, "Maas, Sarah J - A Court of Thorns and Roses", true},
		{"The Hobbit", "J. R. R. Tolkien", "J.R.R. Tolkien - The Hobbit (Read by Andy Serkis) MP3", true},

		// one-word titles: other books that contain the word are not it
		{"Dune", "Frank Herbert", "Frank Herbert - Dune (1965) EPUB", true},
		{"Dune", "Frank Herbert", "Frank Herbert - Dune Messiah EPUB", false},
		{"Dune", "Frank Herbert", "Frank Herbert - Children of Dune EPUB", false},
		{"Dune", "Frank Herbert", "Dune - Frank Herbert", true},
		{"It", "Stephen King", "Stephen King - It", true},

		// subtitles may be left off, or written out
		{"Project Hail Mary: A Novel", "Andy Weir", "Andy Weir - Project Hail Mary EPUB", true},
		{"Project Hail Mary: A Novel", "Andy Weir", "Project.Hail.Mary.A.Novel.Andy.Weir.EPUB", true},

		// punctuation and case don't matter; numbers in the title still count
		{"Ender's Game", "Orson Scott Card", "ENDERS GAME - Orson Scott Card", true},
		{"1984", "George Orwell", "George Orwell - 1984 (Penguin Modern Classics) epub", true},
		{"Fahrenheit 451", "Ray Bradbury", "Ray Bradbury - Fahrenheit 451 - 60th Anniversary Edition", true},
		{"Spider-Man: Forever Young", "Stefan Petrucha", "Spider-Man - Forever Young", true},
	}
	for _, c := range cases {
		if got := TitleMatches(c.book, c.author, c.release); got != c.want {
			t.Errorf("TitleMatches(%q, %q)\n  release %q = %v, want %v", c.book, c.author, c.release, got, c.want)
		}
	}
}
