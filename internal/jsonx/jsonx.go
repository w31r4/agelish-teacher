//go:build !goexperiment.jsonv2

package jsonx

import (
	"encoding/json"
	"io"
)

type Decoder = json.Decoder
type Encoder = json.Encoder

func Marshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

func MarshalIndent(v any, prefix string, indent string) ([]byte, error) {
	return json.MarshalIndent(v, prefix, indent)
}

func Unmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

func NewDecoder(r io.Reader) *Decoder {
	return json.NewDecoder(r)
}

func NewEncoder(w io.Writer) *Encoder {
	return json.NewEncoder(w)
}
