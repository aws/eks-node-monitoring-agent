// chroot wrapper written using go because we dont have chroot on the eks node
// monitoring agent docker image.

package main

import (
	"os"

	"github.com/aws/eks-node-monitoring-agent/pkg/osext"
)

func main() {
	cmd := osext.NewExec(os.Args[1]).Command(os.Args[2], os.Args[3:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		panic(err)
	}
}
