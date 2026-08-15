package api

import (
	"encoding/json"
	"fmt"
	"net/http"
)

const maxBodySize = 1 << 20 // requests are small JSON; anything bigger is hostile

func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBodySize))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid request body: %w", err)
	}
	return nil
}
