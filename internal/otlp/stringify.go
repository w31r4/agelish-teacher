package otlp

import (
	"encoding/json"
	"fmt"
)

func stringsTrimJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(raw)
}
