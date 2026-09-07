package v1alpha1

// SetDefaults fills zero-valued wrapper fields. Defaulting for the
// embedded kubeadm documents is handled by kubeadm's own defaulters
// during config.Load (BytesToInitConfiguration); nothing in this
// package should attempt to mirror that work.
func SetDefaults(c *PicoKubeConfig) {
	if c.APIVersion == "" {
		c.APIVersion = APIVersion
	}
	if c.Kind == "" {
		c.Kind = Kind
	}
}

// NewDefault returns a PicoKubeConfig wrapper with apiVersion / kind
// populated and an empty Spec. Used by `config print-defaults` as the
// seed before the surrounding kubeadm documents are appended.
func NewDefault() *PicoKubeConfig {
	c := &PicoKubeConfig{
		Metadata: ObjectMeta{Name: "local"},
	}
	SetDefaults(c)
	return c
}
