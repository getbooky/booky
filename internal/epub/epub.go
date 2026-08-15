// Package epub reads embedded metadata from EPUB files (OPF inside the zip),
// including the calibre-convention series fields that KoReader and calibre use.
package epub

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
)

type Metadata struct {
	Title       string
	Authors     []string
	Description string
	Language    string
	Publisher   string
	Date        string
	SeriesName  string
	SeriesIndex float64
	ISBN        string
	GoodreadsID string
	HardcoverID string
	// CoverPath is a local image file to embed on Write; Read never sets it.
	CoverPath string
}

const maxOPFSize = 4 << 20

// Read extracts metadata from the EPUB at fsPath. It reads only container.xml
// and the OPF — never the content documents.
func Read(fsPath string) (*Metadata, error) {
	zr, err := zip.OpenReader(fsPath)
	if err != nil {
		return nil, fmt.Errorf("open epub: %w", err)
	}
	defer zr.Close()
	return read(&zr.Reader)
}

func read(zr *zip.Reader) (*Metadata, error) {
	opfPath, err := opfPath(zr)
	if err != nil {
		return nil, err
	}
	opfFile, err := openZipFile(zr, opfPath)
	if err != nil {
		return nil, fmt.Errorf("open opf %s: %w", opfPath, err)
	}
	defer opfFile.Close()

	var pkg opfPackage
	if err := xml.NewDecoder(io.LimitReader(opfFile, maxOPFSize)).Decode(&pkg); err != nil {
		return nil, fmt.Errorf("parse opf: %w", err)
	}
	return pkg.toMetadata(), nil
}

func opfPath(zr *zip.Reader) (string, error) {
	f, err := openZipFile(zr, "META-INF/container.xml")
	if err != nil {
		return "", fmt.Errorf("container.xml: %w", err)
	}
	defer f.Close()

	var container struct {
		Rootfiles []struct {
			FullPath string `xml:"full-path,attr"`
		} `xml:"rootfiles>rootfile"`
	}
	if err := xml.NewDecoder(io.LimitReader(f, maxOPFSize)).Decode(&container); err != nil {
		return "", fmt.Errorf("parse container.xml: %w", err)
	}
	if len(container.Rootfiles) == 0 || container.Rootfiles[0].FullPath == "" {
		return "", fmt.Errorf("no rootfile in container.xml")
	}
	return container.Rootfiles[0].FullPath, nil
}

func openZipFile(zr *zip.Reader, name string) (io.ReadCloser, error) {
	for _, f := range zr.File {
		if path.Clean(f.Name) == path.Clean(name) {
			return f.Open()
		}
	}
	return nil, fmt.Errorf("%s not found", name)
}

type opfPackage struct {
	Metadata struct {
		Titles      []string        `xml:"title"`
		Creators    []opfCreator    `xml:"creator"`
		Description string          `xml:"description"`
		Languages   []string        `xml:"language"`
		Publisher   string          `xml:"publisher"`
		Dates       []string        `xml:"date"`
		Identifiers []opfIdentifier `xml:"identifier"`
		Metas       []opfMeta       `xml:"meta"`
	} `xml:"metadata"`
}

type opfCreator struct {
	Name string `xml:",chardata"`
	Role string `xml:"role,attr"`
}

type opfIdentifier struct {
	Value  string `xml:",chardata"`
	Scheme string `xml:"scheme,attr"`
	ID     string `xml:"id,attr"`
}

type opfMeta struct {
	Name     string `xml:"name,attr"`
	Content  string `xml:"content,attr"`
	Property string `xml:"property,attr"`
	Value    string `xml:",chardata"`
}

func (p opfPackage) toMetadata() *Metadata {
	m := &Metadata{
		Description: strings.TrimSpace(p.Metadata.Description),
		Publisher:   strings.TrimSpace(p.Metadata.Publisher),
	}
	if len(p.Metadata.Titles) > 0 {
		m.Title = strings.TrimSpace(p.Metadata.Titles[0])
	}
	if len(p.Metadata.Languages) > 0 {
		m.Language = strings.TrimSpace(p.Metadata.Languages[0])
	}
	if len(p.Metadata.Dates) > 0 {
		m.Date = strings.TrimSpace(p.Metadata.Dates[0])
	}
	for _, c := range p.Metadata.Creators {
		// aut = author; creators without a role are usually authors too
		if c.Role == "" || c.Role == "aut" {
			if name := strings.TrimSpace(c.Name); name != "" {
				m.Authors = append(m.Authors, name)
			}
		}
	}
	for _, id := range p.Metadata.Identifiers {
		value := strings.TrimSpace(id.Value)
		scheme := strings.ToUpper(id.Scheme)
		switch {
		case scheme == "ISBN" || strings.HasPrefix(strings.ToLower(value), "urn:isbn:"):
			m.ISBN = strings.TrimPrefix(strings.ToLower(value), "urn:isbn:")
			m.ISBN = strings.TrimSpace(m.ISBN)
		case scheme == "GOODREADS":
			m.GoodreadsID = value
		case scheme == "HARDCOVER":
			m.HardcoverID = value
		case m.ISBN == "" && looksLikeISBN(value):
			m.ISBN = value
		}
	}
	for _, meta := range p.Metadata.Metas {
		switch {
		case meta.Name == "calibre:series":
			m.SeriesName = meta.Content
		case meta.Name == "calibre:series_index":
			m.SeriesIndex, _ = strconv.ParseFloat(meta.Content, 64)
		case meta.Property == "belongs-to-collection" && m.SeriesName == "":
			m.SeriesName = strings.TrimSpace(meta.Value)
		}
	}
	return m
}

func looksLikeISBN(s string) bool {
	s = strings.ReplaceAll(s, "-", "")
	if len(s) != 10 && len(s) != 13 {
		return false
	}
	for i, r := range s {
		if r < '0' || r > '9' {
			// ISBN-10 may end in X
			if len(s) != 10 || i != 9 || (r != 'X' && r != 'x') {
				return false
			}
		}
	}
	return true
}
