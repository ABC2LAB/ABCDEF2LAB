//go:build !linux

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "budget approval is available only on Linux")
	os.Exit(1)
}
