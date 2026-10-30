package enroll

import (
	"context"
	"fmt"
	"k8nnect/internal/kube"
)

func applyOwned(ctx context.Context, k *kube.Client, c Config, collection, name string, obj kube.Object) error {
	metadata := kube.Map(obj, "metadata")
	if metadata == nil {
		return fmt.Errorf("managed resource lacks metadata")
	}
	labels := kube.Map(metadata, "labels")
	if labels == nil {
		labels = kube.Object{}
		metadata["labels"] = labels
	}
	labels["k8nnect.io/connection"] = c.Name
	if existing, e := k.Get(ctx, collection+"/"+name); e == nil {
		if kube.Str(kube.Map(kube.Map(existing, "metadata"), "labels"), "k8nnect.io/connection") != c.Name {
			return fmt.Errorf("refusing to overwrite foreign resource %s/%s", collection, name)
		}
	} else if !kube.Code(e, 404) {
		return e
	}
	return k.Apply(ctx, collection, name, obj)
}
