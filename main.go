// qvr is a CLI-native agent skills manager: git repos as registries,
// symlinks as installs. main delegates straight to cmd.Execute.
package main

import "github.com/astra-sh/qvr/cmd"

func main() {
	cmd.Execute()
}
