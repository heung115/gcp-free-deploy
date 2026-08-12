package main

import (
	"context"
	"fmt"
	"os"
)

func main() {
	if err := runCLI(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr, ExecRunner{}, "."); err != nil {
		fmt.Fprintf(os.Stderr, "\n오류: %v\n", err)
		os.Exit(1)
	}
}
