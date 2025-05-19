package main

import (
	"context"
	"flag"
	"log"

	"github.com/cloudferro/terraform-provider-cloudferro/cloudferro"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
)

// Run "go generate" to format example terraform files and generate the docs for the registry/website

// If you do not have terraform installed, you can remove the formatting command, but its suggested to
// ensure the documentation is formatted properly.
//go:generate terraform fmt -recursive ./examples/

// Run the docs generation tool, check its repository for more information on how it works and how docs
// can be customized.
//go:generate go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs generate -provider-name cloudferro

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
