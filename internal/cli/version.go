package cli

import (
	"fmt"
	"io"
)

// Version is the current commander version. Bump on release.
const Version = "0.0.1"

// PrintVersion writes the version line to w in the form expected by the
// `cmdr --version` contract.
func PrintVersion(w io.Writer) {
	fmt.Fprintf(w, "commander %s\n", Version)
}
