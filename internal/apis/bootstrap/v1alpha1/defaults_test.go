package v1alpha1

import "testing"

func TestSetDefaults_FillsWrapperTypeMeta(t *testing.T) {
	c := &PicoKubeConfig{}
	SetDefaults(c)
	if c.APIVersion != APIVersion {
		t.Errorf("APIVersion = %q, want %q", c.APIVersion, APIVersion)
	}
	if c.Kind != Kind {
		t.Errorf("Kind = %q, want %q", c.Kind, Kind)
	}
}

func TestSetDefaults_PreservesUserValues(t *testing.T) {
	c := &PicoKubeConfig{TypeMeta: TypeMeta{APIVersion: "other/v1", Kind: "Other"}}
	SetDefaults(c)
	if c.APIVersion != "other/v1" {
		t.Errorf("APIVersion overwritten: got %q", c.APIVersion)
	}
	if c.Kind != "Other" {
		t.Errorf("Kind overwritten: got %q", c.Kind)
	}
}

func TestNewDefault_PopulatesTypeMeta(t *testing.T) {
	c := NewDefault()
	if c.APIVersion != APIVersion {
		t.Errorf("APIVersion = %q, want %q", c.APIVersion, APIVersion)
	}
	if c.Kind != Kind {
		t.Errorf("Kind = %q, want %q", c.Kind, Kind)
	}
}
