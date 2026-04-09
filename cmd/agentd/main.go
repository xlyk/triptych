package main

import (
	"fmt"
	"os"
)

func main() {
	if _, err := fmt.Fprintln(os.Stdout, "agentd: not implemented"); err != nil {
		os.Exit(1)
	}
}
