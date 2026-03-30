//go:build mage

package main

import (
	"context"
	"os"

	"github.com/magefile/mage/mg"

	//mage:import
	"github.com/dosquad/mage"

	"github.com/dosquad/mage/dyndep"
)

var Default = TestLocal

func init() {
	os.Setenv("CGO_ENABLED", "1")
	os.Setenv("PLUGINS_DIR", "./artifacts/plugins")
	dyndep.Add(dyndep.Build, Plugin.BuildAll)
	dyndep.Add(dyndep.Run, Plugin.BuildAll)
}

// TestLocal update, protoc, format, tidy, lint & test.
func TestLocal(ctx context.Context) {
	mg.SerialCtxDeps(ctx, mage.Golang.Lint, mage.Golang.Test)
}
