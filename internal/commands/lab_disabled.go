//go:build !replab

package commands

import (
	"github.com/spf13/cobra"
)

const labBuild = false

func configureBuildHelp(*cobra.Command) {}
