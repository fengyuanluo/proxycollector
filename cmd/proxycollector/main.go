package main

import (
	"context"
	"os"

	"github.com/fengyuanluo/proxycollector/internal/app"
)

func main() {
	os.Exit(app.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}
