package api

import (
	_ "embed"

	"github.com/awslabs/operatorpkg/object"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

//go:generate controller-gen crd object paths="./..." output:crd:artifacts:config=crds
var (
	//go:embed crds/eks.amazonaws.com_nodediagnostics.yaml
	NodeDiagnosticCRDBytes []byte
	CRDs                   = []*apiextensionsv1.CustomResourceDefinition{
		object.Unmarshal[apiextensionsv1.CustomResourceDefinition](NodeDiagnosticCRDBytes),
	}
)
