package main

import (
	"os"

	"github.com/kminiatures/aitodo/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
