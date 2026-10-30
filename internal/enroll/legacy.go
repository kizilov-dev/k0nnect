package enroll

import (
	"context"
	"fmt"

	"k0nnect/internal/kube"
)

// The old project name is deliberately retained only for migration detection.
// Its ownership labels and host state cannot be adopted by a textual rename.
func rejectLegacyInstallation(ctx context.Context, k *kube.Client) error {
	namespaces, err := k.Get(ctx, "/api/v1/namespaces?labelSelector=app.kubernetes.io%2Fmanaged-by%3Dk8nnect")
	if kube.Code(err, 404) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, value := range kube.Items(namespaces, "items") {
		name := kube.Str(kube.Map(kube.As(value), "metadata"), "name")
		return fmt.Errorf("legacy installation in namespace %s: drain and disconnect with the old binary before installing k0nnect; see the upgrade guide", name)
	}
	return nil
}
