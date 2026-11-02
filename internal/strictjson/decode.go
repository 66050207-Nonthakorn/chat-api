// Package strictjson decodes exactly one JSON value without unknown fields.
package strictjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// Decode rejects unknown fields and non-whitespace after the first JSON value.
func Decode(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("multiple JSON values")
	}
	return nil
}
