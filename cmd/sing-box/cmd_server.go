package main

import (
	"fmt"
	"os"

	"github.com/sagernet/sing-box/experimental/panelcompat"
	"github.com/sagernet/sing-box/log"

	"github.com/spf13/cobra"
)

var (
	serverConfigPath string
	serverStatePath  string
	serverCheckOnly  bool
)

var commandServer = &cobra.Command{
	Use:   "server",
	Short: "Run AnyTLS nodes from a panel server.yml",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		if len(configPaths) != 1 || len(configDirectories) != 0 {
			log.Fatal("server mode requires exactly one -c/--config and does not support -C")
		}
		result, err := panelcompat.Run(panelcompat.RunOptions{
			Context:          globalCtx,
			BaseConfigPath:   configPaths[0],
			ServerConfigPath: serverConfigPath,
			StateDirectory:   serverStatePath,
			CheckOnly:        serverCheckOnly,
		})
		if err != nil {
			log.Fatal(err)
		}
		if serverCheckOnly {
			configPaths = []string{result.RuntimePath}
			configDirectories = nil
			if err = check(); err != nil {
				log.Fatal(err)
			}
			fmt.Fprintf(os.Stdout, "configuration is valid: %d AnyTLS node(s), %d user(s)\nruntime: %s\n", result.NodeCount, result.UserCount, result.RuntimePath)
		}
	},
}

func init() {
	commandServer.Flags().StringVarP(&serverConfigPath, "server-config", "s", "server.yml", "set panel server.yml path")
	commandServer.Flags().StringVar(&serverStatePath, "state-directory", "", "set generated state directory")
	commandServer.Flags().BoolVar(&serverCheckOnly, "check", false, "fetch panel data and validate without starting")
	mainCommand.AddCommand(commandServer)
}
