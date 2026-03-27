//go:build mage

package main

import (
	"context"

	"github.com/dosquad/mage/helper/paths"
	"github.com/magefile/mage/mg"
	"github.com/princjef/mageutil/shellcmd"
)

type Plugin mg.Namespace

func (Plugin) BuildAll(ctx context.Context) error {
	mg.Deps(Plugin.BuildDNS)
	return nil
}

func (Plugin) BuildDNS(ctx context.Context) error {
	pluginPath := paths.MustGetArtifactPath("plugins")
	if err := shellcmd.Command("go build -o " + pluginPath + "/dns.so -buildmode=plugin ./plugins/dns/").Run(); err != nil {
		return err
	}
	return nil
}
