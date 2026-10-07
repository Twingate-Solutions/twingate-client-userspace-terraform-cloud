// Command terraform-provider-twingatetunnel is a Terraform provider. When
// started with TWINGATETUNNEL_SUPERVISOR set it instead runs as the tunnel
// supervisor process forked by the provider.
package main

import (
	"context"
	"flag"
	"log"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/Twingate-Solutions/twingate-client-userspace-terraform-cloud/internal/provider"
	"github.com/Twingate-Solutions/twingate-client-userspace-terraform-cloud/internal/supervisor"
)

// version is set by GoReleaser.
var version = "dev"

func main() {
	if os.Getenv(supervisor.ModeEnv) != "" {
		if err := supervisor.Main(); err != nil {
			log.Fatal(err)
		}
		return
	}

	var debug bool
	flag.BoolVar(&debug, "debug", false, "run the provider with support for debuggers like delve")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/twingate-solutions/twingatetunnel",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err)
	}
}
