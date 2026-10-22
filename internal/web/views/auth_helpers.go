package views

import "encoding/json"

func csrfHeaders(token string) string {
	value, _ := json.Marshal(map[string]string{"X-CSRF-Token": token})
	return string(value)
}
