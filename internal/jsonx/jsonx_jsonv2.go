//go:build goexperiment.jsonv2

package jsonx

import (
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"io"
)

type Decoder struct {
	r io.Reader
}

type Encoder struct {
	w io.Writer
}

func Marshal(v any) ([]byte, error) {
	return jsonv2.Marshal(v)
}

func MarshalIndent(v any, prefix string, indent string) ([]byte, error) {
	return jsonv2.Marshal(v, jsontext.WithIndentPrefix(prefix), jsontext.WithIndent(indent))
}

func Unmarshal(data []byte, v any) error {
	return jsonv2.Unmarshal(data, v)
}

func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{r: r}
}

func (d *Decoder) Decode(v any) error {
	return jsonv2.UnmarshalRead(d.r, v)
}

func NewEncoder(w io.Writer) *Encoder {
	return &Encoder{w: w}
}

func (e *Encoder) Encode(v any) error {
	if err := jsonv2.MarshalWrite(e.w, v); err != nil {
		return err
	}
	_, err := e.w.Write([]byte("\n"))
	return err
}
