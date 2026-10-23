package refdata

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// CanonicalBytes serializes the normalized document deterministically.
//
// Go's encoding/json emits struct fields in declaration order and does not sort
// struct fields at runtime. Every slice is sorted by Normalize, so two
// semantically identical documents produce identical bytes in Go. The Rust SDK
// verifies and hashes these exact bytes rather than re-serializing them.
func (d Document) CanonicalBytes() ([]byte, error) {
	normalized := d.Normalize()
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("marshal reference-data document: %w", err)
	}
	return encoded, nil
}

// Parse strictly decodes a canonical document. Unknown fields are rejected so a
// newer schema can never be silently ignored.
func Parse(raw []byte) (Document, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var document Document
	if err := decoder.Decode(&document); err != nil {
		return Document{}, fmt.Errorf("decode reference-data document: %w", err)
	}
	if decoder.More() {
		return Document{}, fmt.Errorf("decode reference-data document: trailing data")
	}
	return document.Normalize(), nil
}
