package config

import "os"

func writeFile(p, body string) error {
	return os.WriteFile(p, []byte(body), 0o600)
}
