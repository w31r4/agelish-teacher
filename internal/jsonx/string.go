package jsonx

import "fmt"

func String(v any) string {
	raw, err := Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(raw)
}
