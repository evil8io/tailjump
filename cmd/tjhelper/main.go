// Command tjhelper is the remote-side binary that a tj session uploads and
// runs on the remote. It must import no package that imports tailscale.com,
// cobra, or gvisor.
package main

import "github.com/evil8io/tailjump/internal/helper"

func main() {
	helper.Main()
}
