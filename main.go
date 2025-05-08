package main

import (
	"context"
	"flag"
	"log"

	"github.com/cloudferro/terraform-provider-cloudferro/cloudferro"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
)

const version = "dev"

func main() {
	var debug bool

	flag.BoolVar(&debug, "debug", false, "set to true to run the provider with debug logs")
	flag.Parse()

	err := providerserver.Serve(
		context.Background(),
		cloudferro.NewProvider(version),
		providerserver.ServeOpts{
			Address: "registry.terraform.io/cloudferro/terraform-provider-cloudferro",
			Debug:   debug,
		},
	)
	if err != nil {
		log.Fatal(err)
	}
}
