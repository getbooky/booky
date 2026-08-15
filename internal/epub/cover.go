package epub

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"strings"
)

// coverOPF is the manifest-focused view of the OPF: enough to locate the
// cover image by either convention (EPUB 3 properties="cover-image", or the
// EPUB 2 <meta name="cover"> pointing at a manifest id).
type coverOPF struct {
	Metadata struct {
		Metas []opfMeta `xml:"meta"`
	} `xml:"metadata"`
	Manifest struct {
		Items []struct {
			ID         string `xml:"id,attr"`
			Href       string `xml:"href,attr"`
			MediaType  string `xml:"media-type,attr"`
			Properties string `xml:"properties,attr"`
		} `xml:"item"`
	} `xml:"manifest"`
}

// Cover extracts the embedded cover image from an epub, returning the image
// bytes and their media type. Books written by Booky's own metadata pass
// carry one; so do most retail epubs.
func Cover(fsPath string) ([]byte, string, error) {
	zr, err := zip.OpenReader(fsPath)
	if err != nil {
		return nil, "", err
	}
	defer zr.Close()

	opf, err := opfPath(&zr.Reader)
	if err != nil {
		return nil, "", err
	}
	rc, err := openZipFile(&zr.Reader, opf)
	if err != nil {
		return nil, "", err
	}
	raw, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		return nil, "", err
	}
	var pkg coverOPF
	if err := xml.Unmarshal(raw, &pkg); err != nil {
		return nil, "", fmt.Errorf("parse opf: %w", err)
	}

	href, mediaType := "", ""
	for _, it := range pkg.Manifest.Items {
		if strings.Contains(it.Properties, "cover-image") {
			href, mediaType = it.Href, it.MediaType
			break
		}
	}
	if href == "" {
		coverID := ""
		for _, m := range pkg.Metadata.Metas {
			if m.Name == "cover" && m.Content != "" {
				coverID = m.Content
				break
			}
		}
		for _, it := range pkg.Manifest.Items {
			if coverID != "" && it.ID == coverID && strings.HasPrefix(it.MediaType, "image/") {
				href, mediaType = it.Href, it.MediaType
				break
			}
		}
	}
	if href == "" {
		return nil, "", fmt.Errorf("no cover in %s", path.Base(fsPath))
	}

	// hrefs are relative to the OPF's directory
	img, err := openZipFile(&zr.Reader, path.Join(path.Dir(opf), href))
	if err != nil {
		return nil, "", err
	}
	defer img.Close()
	data, err := io.ReadAll(io.LimitReader(img, 10<<20))
	if err != nil {
		return nil, "", err
	}
	if mediaType == "" {
		mediaType = "image/jpeg"
	}
	return data, mediaType, nil
}
