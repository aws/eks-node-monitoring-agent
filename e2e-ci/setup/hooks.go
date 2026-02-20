package setup

import (
	"context"
	"log"
	"slices"

	frameworkext "github.com/aws/eks-node-monitoring-agent/e2e-ci/framework_extensions"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/types"
)

// makeInstallAgentHooks creates setup and finish functions that install/uninstall
// the node monitoring agent manifests using the provided image.
func makeInstallAgentHooks(image string) (setupFn, finishFn types.EnvFunc) {
	agentManifest, err := frameworkext.RenderManifests(agentManifestTemplateData, struct{ Image string }{
		Image: image,
	})
	if err != nil {
		log.Fatalf("failed to render agent manifest template: %v", err)
	}

	manifests := [][]byte{agentManifest}

	setupFn = func(ctx context.Context, config *envconf.Config) (context.Context, error) {
		log.Printf("installing node monitoring agent manifests...")
		if err := frameworkext.ApplyManifests(config.Client().RESTConfig(), manifests...); err != nil {
			return ctx, err
		}
		log.Printf("manifests applied successfully")
		return ctx, nil
	}

	finishFn = func(ctx context.Context, config *envconf.Config) (context.Context, error) {
		log.Printf("cleaning up node monitoring agent manifests...")
		slices.Reverse(manifests)
		if err := frameworkext.DeleteManifests(config.Client().RESTConfig(), manifests...); err != nil {
			return ctx, err
		}
		log.Printf("manifests deleted successfully")
		return ctx, nil
	}

	return setupFn, finishFn
}
