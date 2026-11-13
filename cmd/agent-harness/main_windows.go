package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "agent-harness は Windows に対応していません")
	os.Exit(1)
}
